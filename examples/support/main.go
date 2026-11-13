package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"

	_ "github.com/lib/pq"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/postgres"
)

func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatal("Missing environment variable ", name)
	}
	return value
}

func database(name string) *sql.DB {
	db, err := sql.Open("postgres", required(name))
	if err != nil {
		log.Fatal("Invalid database configuration: ", name)
	}
	return db
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func respondError(w http.ResponseWriter, err error) {
	var sdk *heimdall.Error
	code, status := "unavailable", http.StatusServiceUnavailable
	if errors.As(err, &sdk) {
		code = string(sdk.Code)
		switch code {
		case "unauthenticated":
			status = http.StatusUnauthorized
		case "forbidden":
			status = http.StatusForbidden
		case "invalid_argument":
			status = http.StatusBadRequest
		case "not_found":
			status = http.StatusNotFound
		}
	}
	writeJSON(w, status, map[string]string{"code": code})
}

func main() {
	configJSON, manifestJSON := []byte(required("HEIMDALL_CONFIG")), []byte(required("HEIMDALL_MANIFEST"))
	var config struct{ ProductID, Environment string }
	if json.Unmarshal(configJSON, &config) != nil || config.ProductID != "support" {
		log.Fatal("Expected the Support product")
	}
	callback := required("HEIMDALL_CALLBACK_URL")
	callbackURL, err := url.Parse(callback)
	if err != nil {
		log.Fatal("Invalid callback configuration")
	}
	records, keyUsage, business := database("DATABASE_URL"), database("HEIMDALL_KEY_DATABASE_URL"), database("SUPPORT_DATABASE_URL")
	defer records.Close()
	defer keyUsage.Close()
	defer business.Close()
	var keys map[string][]byte
	if json.Unmarshal([]byte(required("HEIMDALL_RECORD_KEYS")), &keys) != nil {
		log.Fatal("Invalid record keys")
	}
	storage, err := postgres.NewManagedStorage(records, postgres.StorageOptions{
		ProductID: config.ProductID, Environment: config.Environment,
		Sessions: &postgres.SessionStorageOptions{KeyUsageDB: keyUsage, ActiveKeyID: required("HEIMDALL_RECORD_KEY_ID"), Keys: keys},
	})
	if err != nil {
		log.Fatal("Invalid Heimdall storage configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	auth, err := heimdall.New(ctx, configJSON, manifestJSON, heimdall.Options{
		PolicyVersion: required("HEIMDALL_POLICY_VERSION"), Storage: storage,
		Credentials: heimdall.Credentials{
			ClientSecret: func(context.Context) (string, error) { return required("HEIMDALL_CLIENT_SECRET"), nil },
			OpenFGAKey:   func(context.Context) (string, error) { return required("HEIMDALL_OPENFGA_KEY"), nil },
		},
	})
	if err != nil {
		log.Fatal("Heimdall startup: ", err)
	}
	sessions, err := auth.NewSessions(ctx, heimdall.SessionOptions{CallbackURL: callback, ReturnTo: "/workspaces", ZitadelAPIAudience: required("HEIMDALL_ZITADEL_API_AUDIENCE")})
	if err != nil {
		log.Fatal("Session startup: ", err)
	}
	cursor, err := base64.StdEncoding.DecodeString(required("HEIMDALL_CURSOR_KEY"))
	if err != nil {
		log.Fatal("Invalid cursor key")
	}
	discovery, err := auth.NewWorkspaceDiscovery(heimdall.DiscoveryOptions{ActiveKeyID: "v1", Keys: map[string][]byte{"v1": cursor}})
	if err != nil {
		log.Fatal("Discovery startup: ", err)
	}
	mux := http.NewServeMux()
	mount := func(pattern string, handler func(http.ResponseWriter, *http.Request) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			if err := handler(w, r.WithContext(ctx)); err != nil {
				respondError(w, err)
			}
		})
	}
	mount("GET /healthz", func(w http.ResponseWriter, _ *http.Request) error {
		writeJSON(w, 200, map[string]any{
			"ready": true, "deployment": os.Getenv("RAILWAY_DEPLOYMENT_ID"),
			"verificationRun": os.Getenv("HEIMDALL_VERIFICATION_RUN"),
		})
		return nil
	})
	mount("GET /{$}", func(w http.ResponseWriter, _ *http.Request) error {
		writeJSON(w, 200, map[string]string{"login": "/auth/start", "workspaces": "/workspaces"})
		return nil
	})
	mount("GET /auth/start", sessions.Start)
	mount("GET "+callbackURL.Path, sessions.Callback)
	mount("POST /auth/logout", sessions.Logout)
	mount("GET /workspaces", func(w http.ResponseWriter, r *http.Request) error {
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		page, err := discovery.List(r.Context(), actor, heimdall.WorkspaceListOptions{Cursor: r.URL.Query().Get("cursor")})
		if err != nil {
			return err
		}
		items := make([]map[string]string, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, map[string]string{"id": item.ID, "name": item.Name})
		}
		var next any
		if page.NextCursor != "" {
			next = page.NextCursor
		}
		writeJSON(w, 200, map[string]any{"items": items, "nextCursor": next})
		return nil
	})
	workspaceID := regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	mount("GET /workspaces/{workspace}/roles", func(w http.ResponseWriter, r *http.Request) error {
		workspace := r.PathValue("workspace")
		if !workspaceID.MatchString(workspace) {
			return heimdall.ErrInvalidArgument
		}
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		roles, err := auth.Tenant(workspace).Roles(r.Context(), actor)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"roles": roles})
		return nil
	})
	mount("GET /workspaces/{workspace}/tickets", func(w http.ResponseWriter, r *http.Request) error {
		workspace := r.PathValue("workspace")
		if !workspaceID.MatchString(workspace) {
			return heimdall.ErrInvalidArgument
		}
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		if err = auth.Tenant(workspace).Require(r.Context(), actor, "ticket.read"); err != nil {
			return err
		}
		rows, err := business.QueryContext(r.Context(), "SELECT ticket_id,summary FROM support.tickets WHERE workspace_id=$1 ORDER BY ticket_id LIMIT 100", workspace)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := make([]map[string]string, 0)
		for rows.Next() {
			var id, summary string
			if err := rows.Scan(&id, &summary); err != nil {
				return err
			}
			items = append(items, map[string]string{"ticket_id": id, "summary": summary})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"tickets": items})
		return nil
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 20 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal("Support HTTP server stopped")
	}
}
