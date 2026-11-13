package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
	var operationID string
	if errors.As(err, &sdk) {
		code = string(sdk.Code)
		operationID = sdk.OperationID
		switch code {
		case "unauthenticated":
			status = http.StatusUnauthorized
		case "forbidden":
			status = http.StatusForbidden
		case "invalid_argument":
			status = http.StatusBadRequest
		case "conflict":
			status = http.StatusConflict
		case "not_found":
			status = http.StatusNotFound
		}
	}
	body := map[string]string{"code": code}
	if operationID != "" {
		body["operationId"] = operationID
	}
	writeJSON(w, status, body)
}

func mutationInput(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, string, error) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || strings.Contains(keys[0], ", ") {
		return nil, "", heimdall.ErrInvalidArgument
	}
	fields, err := jsonInput(w, r)
	return fields, keys[0], err
}

func jsonInput(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, heimdall.ErrInvalidArgument
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	var fields map[string]json.RawMessage
	if err != nil || !utf8.Valid(body) || json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil, heimdall.ErrInvalidArgument
	}
	return fields, nil
}

func workspaceInput(w http.ResponseWriter, r *http.Request) (heimdall.WorkspaceCreateInput, error) {
	fields, key, err := mutationInput(w, r)
	if err != nil {
		return heimdall.WorkspaceCreateInput{}, err
	}
	var name string
	if len(fields) != 1 || json.Unmarshal(fields["name"], &name) != nil {
		return heimdall.WorkspaceCreateInput{}, heimdall.ErrInvalidArgument
	}
	return heimdall.WorkspaceCreateInput{Name: name, IdempotencyKey: key}, nil
}

func main() {
	configJSON, manifestJSON := []byte(required("HEIMDALL_CONFIG")), []byte(required("HEIMDALL_MANIFEST"))
	var config struct{ ProductID, Environment, Issuer string }
	if json.Unmarshal(configJSON, &config) != nil || config.ProductID != "support" {
		log.Fatal("Expected the Support product")
	}
	callback := required("HEIMDALL_CALLBACK_URL")
	callbackURL, err := url.Parse(callback)
	if err != nil {
		log.Fatal("Invalid callback configuration")
	}
	origin := callbackURL.Scheme + "://" + callbackURL.Host
	workspaceCreator := os.Getenv("HEIMDALL_WORKSPACE_CREATOR_SUBJECT")
	records, keyUsage, business := database("DATABASE_URL"), database("HEIMDALL_KEY_DATABASE_URL"), database("SUPPORT_DATABASE_URL")
	defer records.Close()
	defer keyUsage.Close()
	defer business.Close()
	var keys map[string][]byte
	if json.Unmarshal([]byte(required("HEIMDALL_RECORD_KEYS")), &keys) != nil {
		log.Fatal("Invalid record keys")
	}
	maintenance, err := postgres.NewRecordMaintenance(records, config.ProductID, config.Environment)
	if err != nil {
		log.Fatal("Invalid maintenance configuration")
	}
	var lastSuccessAt atomic.Int64
	var exportDB *sql.DB
	var exportPolicy heimdall.ManagedExportPolicy
	if os.Getenv("HEIMDALL_EXPORT_DATABASE_URL") != "" {
		exportDB = database("HEIMDALL_EXPORT_DATABASE_URL")
		defer exportDB.Close()
		exportPolicy = func(_ context.Context, scope heimdall.ManagedExportScope) (bool, error) {
			return scope.Actor.Subject == workspaceCreator, nil
		}
	}
	storage, err := postgres.NewManagedStorage(records, postgres.StorageOptions{
		ProductID: config.ProductID, Environment: config.Environment, ExportDB: exportDB,
		Sessions: &postgres.SessionStorageOptions{KeyUsageDB: keyUsage, ActiveKeyID: required("HEIMDALL_RECORD_KEY_ID"), Keys: keys},
	})
	if err != nil {
		log.Fatal("Invalid Heimdall storage configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var tenantSSO heimdall.TenantSSOPolicySource
	federationPolicies := os.Getenv("HEIMDALL_TENANT_SSO_POLICIES")
	if federationPolicies != "" {
		var policies map[string]heimdall.TenantSSOPolicy
		if json.Unmarshal([]byte(federationPolicies), &policies) != nil || policies == nil {
			log.Fatal("Invalid federation policies")
		}
		tenantSSO = func(_ context.Context, workspace string) (*heimdall.TenantSSOPolicy, error) {
			policy, found := policies[workspace]
			if !found {
				return nil, nil
			}
			return &policy, nil
		}
	} else if os.Getenv("HEIMDALL_REFERENCE_STRICT_SSO") == "1" {
		tenantSSO = func(_ context.Context, workspaceID string) (*heimdall.TenantSSOPolicy, error) {
			if workspaceID != "globex" {
				return nil, nil
			}
			return &heimdall.TenantSSOPolicy{
				OrganizationID: "reference-globex", ProviderID: "reference-globex-oidc",
				PolicyVersion: "reference-sso-v1", Protocol: "oidc", MaxAgeMS: 900_000,
			}, nil
		}
	}
	auth, err := heimdall.New(ctx, configJSON, manifestJSON, heimdall.Options{
		TenantSSOPolicy: tenantSSO,
		PolicyVersion:   required("HEIMDALL_POLICY_VERSION"), Storage: storage, AuthorizeManagedExport: exportPolicy,
		AuthorizeWorkspaceCreation: func(_ context.Context, actor *heimdall.Principal, _ string) (bool, error) {
			return actor.Identity().Subject == workspaceCreator, nil
		},
		AuthorizeWorkspaceLifecycle: func(_ context.Context, actor *heimdall.Principal, _, _ string) (bool, error) {
			return actor.Identity().Subject == workspaceCreator, nil
		},
		Credentials: heimdall.Credentials{
			ClientSecret: func(context.Context) (string, error) { return required("HEIMDALL_CLIENT_SECRET"), nil },
			OpenFGAKey:   func(context.Context) (string, error) { return required("HEIMDALL_OPENFGA_KEY"), nil },
		},
	})
	if err != nil {
		log.Fatal("Heimdall startup: ", err)
	}
	sessionOptions := heimdall.SessionOptions{CallbackURL: callback, ReturnTo: "/workspaces", ZitadelAPIAudience: required("HEIMDALL_ZITADEL_API_AUDIENCE")}
	if federationPolicies != "" {
		credential := required("HEIMDALL_FEDERATION_CREDENTIAL")
		sessionOptions.FederationCredential = func(context.Context) (string, error) { return credential, nil }
	}
	sessions, err := auth.NewSessions(ctx, sessionOptions)
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
	if federationPolicies != "" {
		mount("GET /auth/federation", func(w http.ResponseWriter, r *http.Request) error {
			workspace := r.URL.Query()["workspace"]
			if len(workspace) != 1 {
				return heimdall.ErrInvalidArgument
			}
			return sessions.StartFederation(w, r, workspace[0])
		})
	}
	mount("GET "+callbackURL.Path, sessions.Callback)
	mount("POST /auth/logout", sessions.Logout)
	mount("GET /workspaces", func(w http.ResponseWriter, r *http.Request) error {
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		options := heimdall.WorkspaceListOptions{Cursor: r.URL.Query().Get("cursor")}
		if r.URL.Query().Has("limit") {
			limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
			if err != nil || limit < 1 || limit > 100 {
				return heimdall.ErrInvalidArgument
			}
			options.Limit = limit
		}
		page, err := discovery.List(r.Context(), actor, options)
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
	onboarding := auth.Onboarding()
	workspaceOperation := func(w http.ResponseWriter, r *http.Request) error {
		mutation := r.Method == http.MethodPost
		if mutation && (len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != origin) {
			return heimdall.ErrForbidden
		}
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		ctx, operation := r.Context(), r.PathValue("operation")
		var result heimdall.WorkspaceOnboardingStatus
		switch {
		case operation == "":
			input, decodeErr := workspaceInput(w, r)
			if decodeErr != nil {
				return decodeErr
			}
			result, err = onboarding.Create(ctx, actor, input)
		case !workspaceID.MatchString(operation):
			return heimdall.ErrInvalidArgument
		case mutation:
			result, err = onboarding.Resume(ctx, actor, operation)
		default:
			result, err = onboarding.Get(ctx, actor, operation)
		}
		if err != nil {
			return err
		}
		status := http.StatusOK
		if mutation && result.Status == "pending" {
			status = http.StatusAccepted
		}
		writeJSON(w, status, result)
		return nil
	}
	mount("POST /workspaces", workspaceOperation)
	mount("GET /workspace-operations/{operation}", workspaceOperation)
	if exportDB != nil {
		mount("GET /managed-export", func(w http.ResponseWriter, r *http.Request) error {
			bearer := strings.Fields(r.Header.Get("Authorization"))
			if len(bearer) != 2 || !strings.EqualFold(bearer[0], "Bearer") {
				return heimdall.ErrUnauthenticated
			}
			query := r.URL.Query()
			input := heimdall.ManagedExportRequest{Kind: query.Get("kind"), OperationID: query.Get("operation"), Limit: 1}
			if query.Has("workspace") {
				value := query.Get("workspace")
				input.WorkspaceID = &value
			}
			if query.Has("after") {
				value := query.Get("after")
				input.After = &value
			}
			export := auth.ExportManagedPage
			if query.Get("inventory") == "1" {
				export = auth.ExportManagedInventory
			}
			data, err := export(r.Context(), bearer[1], input)
			if err != nil {
				return err
			}
			writeJSON(w, 200, json.RawMessage(data))
			return nil
		})
	}
	mount("POST /workspace-operations/reconcile", func(w http.ResponseWriter, r *http.Request) error {
		if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != origin {
			return heimdall.ErrForbidden
		}
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		if actor.Identity().Subject != workspaceCreator {
			return heimdall.ErrForbidden
		}
		result, err := auth.Reconcile(r.Context(), heimdall.ReconcileOptions{})
		if err != nil {
			return err
		}
		lastSuccessAt.Store(time.Now().UnixMilli())
		writeJSON(w, http.StatusOK, result)
		return nil
	})
	mount("GET /workspace-operations/metrics", func(w http.ResponseWriter, r *http.Request) error {
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		if actor.Identity().Subject != workspaceCreator {
			return heimdall.ErrForbidden
		}
		metrics, err := auth.OperationMetrics(r.Context())
		if err != nil {
			return err
		}
		lag, err := maintenance.CleanupLag(r.Context())
		if err != nil {
			return err
		}
		var last any
		if at := lastSuccessAt.Load(); at > 0 {
			last = at
		}
		writeJSON(w, 200, struct {
			heimdall.OperationMetrics
			CleanupLagMS  *int64 `json:"cleanupLagMs"`
			LastSuccessAt any    `json:"lastSuccessAt"`
		}{metrics, lag, last})
		return nil
	})
	mount("POST /workspace-operations/{operation}", workspaceOperation)
	eligibleMember := os.Getenv("HEIMDALL_REFERENCE_MEMBER_SUBJECT")
	invites := invitationHandler(auth, sessions, business, config.Environment, origin, workspaceCreator, eligibleMember, workspaceID)
	mount("POST /workspaces/{workspace}/invitations", invites)
	mount("POST /invitations/accept", invites)
	for _, action := range []string{"suspend", "resume"} {
		mount("POST /workspaces/{workspace}/"+action, func(w http.ResponseWriter, r *http.Request) error {
			if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != origin {
				return heimdall.ErrForbidden
			}
			actor, err := sessions.Authenticate(r)
			if err != nil {
				return err
			}
			tenant := auth.Tenant(r.PathValue("workspace"))
			change := tenant.Suspend
			if action == "resume" {
				change = tenant.Resume
			}
			if err := change(r.Context(), actor); err != nil {
				return err
			}
			w.WriteHeader(http.StatusNoContent)
			return nil
		})
	}
	mount("POST /workspaces/{workspace}/members/{action}", func(w http.ResponseWriter, r *http.Request) error {
		workspace, action := r.PathValue("workspace"), r.PathValue("action")
		takesRoles := action == "add" || action == "assign-roles"
		if !workspaceID.MatchString(workspace) || (!takesRoles && action != "remove" && action != "transfer-ownership") {
			return heimdall.ErrNotFound
		}
		if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != origin {
			return heimdall.ErrForbidden
		}
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		fields, key, err := mutationInput(w, r)
		if err != nil {
			return err
		}
		var subject string
		if json.Unmarshal(fields["subject"], &subject) != nil || subject == "" {
			return heimdall.ErrInvalidArgument
		}
		if subject != workspaceCreator && subject != eligibleMember {
			return heimdall.ErrForbidden
		}
		input := heimdall.MembershipInput{Target: heimdall.Identity{Issuer: config.Issuer, Subject: subject}, IdempotencyKey: key}
		var result heimdall.MembershipStatus
		members := auth.Tenant(workspace).Memberships()
		if takesRoles {
			var roles []string
			if len(fields) != 2 || json.Unmarshal(fields["roles"], &roles) != nil || roles == nil {
				return heimdall.ErrInvalidArgument
			}
			withRoles := heimdall.MembershipRolesInput{MembershipInput: input, Roles: roles}
			if action == "add" {
				result, err = members.Add(r.Context(), actor, withRoles)
			} else {
				result, err = members.AssignRoles(r.Context(), actor, withRoles)
			}
		} else {
			if len(fields) != 1 {
				return heimdall.ErrInvalidArgument
			}
			if action == "remove" {
				result, err = members.Remove(r.Context(), actor, input)
			} else {
				result, err = members.TransferOwnership(r.Context(), actor, input)
			}
		}
		if err != nil {
			return err
		}
		status := http.StatusOK
		if result.Status == "pending" {
			status = http.StatusAccepted
		}
		writeJSON(w, status, result)
		return nil
	})
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
