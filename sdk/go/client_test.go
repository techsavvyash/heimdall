package heimdall_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	postgres "github.com/techsavvyash/heimdall/sdk/go/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type runtimeHarness struct {
	checks           atomic.Int64
	writes           atomic.Int64
	providerFault    atomic.Value
	config, manifest []byte
	options          heimdall.Options
	token            string
	mu               sync.Mutex
	events           []map[string]any
}

func setupRuntime(t *testing.T, failure string, environment ...string) *runtimeHarness {
	t.Helper()
	raw, e := os.ReadFile("../../contracts/fixtures/configuration-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var base struct {
		Config   map[string]any
		Manifest map[string]any
	}
	if json.Unmarshal(raw, &base) != nil {
		t.Fatal("fixtures")
	}
	raw, e = os.ReadFile("../../contracts/fixtures/oidc-discovery.json")
	if e != nil {
		t.Fatal(e)
	}
	var discovery struct{ Metadata map[string]any }
	if json.Unmarshal(raw, &discovery) != nil {
		t.Fatal("metadata")
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	h := &runtimeHarness{}
	h.providerFault.Store("none")
	var tupleMu sync.Mutex
	var tuples []map[string]string
	// Introspection reports this token's expiry, not a sliding response-time expiry.
	expiresAt := time.Now().Add(time.Hour).Unix()
	var issuer string
	var model map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			if failure == "discovery" {
				w.WriteHeader(503)
			}
			body = discovery.Metadata
		case "/stores/store-1/authorization-models/model-1":
			body = map[string]any{"authorization_model": model}
		case "/stores/store-1/read":
			if h.providerFault.Load() == "before_dispatch" {
				w.WriteHeader(503)
				return
			}
			var read struct {
				Key         map[string]string `json:"tuple_key"`
				Consistency string            `json:"consistency"`
			}
			if json.NewDecoder(r.Body).Decode(&read) != nil || read.Consistency != "HIGHER_CONSISTENCY" {
				w.WriteHeader(400)
				return
			}
			matching := []map[string]any{}
			tupleMu.Lock()
			for _, tuple := range tuples {
				if tuple["object"] == read.Key["object"] && tuple["relation"] == read.Key["relation"] &&
					(read.Key["user"] == "" || tuple["user"] == read.Key["user"]) {
					matching = append(matching, map[string]any{"key": tuple})
				}
			}
			tupleMu.Unlock()
			body = map[string]any{"tuples": matching}
		case "/stores/store-1/write":
			var write struct {
				Writes struct {
					Tuples []map[string]string `json:"tuple_keys"`
				} `json:"writes"`
			}
			if json.NewDecoder(r.Body).Decode(&write) != nil {
				w.WriteHeader(400)
				return
			}
			h.writes.Add(1)
			if h.providerFault.Load() == "lost_before_apply" {
				panic(http.ErrAbortHandler)
			}
			tupleMu.Lock()
			tuples = append(tuples, write.Writes.Tuples...)
			tupleMu.Unlock()
			if h.providerFault.Load() == "lost_response" {
				panic(http.ErrAbortHandler)
			}
			body = map[string]any{}
		case "/stores/store-1/check":
			h.checks.Add(1)
			var check struct {
				Model       string `json:"authorization_model_id"`
				Consistency string
				Tuple       map[string]string `json:"tuple_key"`
			}
			user := "user:" + base64.RawURLEncoding.EncodeToString([]byte(`["`+issuer+`","runtime-user"]`))
			if json.NewDecoder(r.Body).Decode(&check) != nil || check.Model != "model-1" ||
				check.Consistency != "HIGHER_CONSISTENCY" || check.Tuple["user"] != user || !slices.Contains([]string{"member", "p_0", "e_0", "e_1", "e_2", "e_3"}, check.Tuple["relation"]) {
				w.WriteHeader(400)
				return
			}
			if check.Tuple["object"] == "workspace:outage" {
				w.WriteHeader(503)
				return
			}
			allowed := check.Tuple["object"] == "workspace:acme" && slices.Contains([]string{"member", "p_0", "e_1"}, check.Tuple["relation"])
			tupleMu.Lock()
			for _, tuple := range tuples {
				allowed = allowed || check.Tuple["object"] == tuple["object"] &&
					check.Tuple["relation"] == "member" && tuple["relation"] == "member"
			}
			tupleMu.Unlock()
			body = map[string]any{"allowed": allowed}
		case "/oauth/v2/keys":
			body = map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "runtime", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}}
		case "/oauth/v2/introspect":
			body = map[string]any{"active": true, "iss": issuer, "sub": "runtime-user", "aud": "ledger-api", "exp": expiresAt}
		case "/v2/users/runtime-user":
			body = map[string]any{"user": map[string]any{"userId": "runtime-user", "state": "USER_STATE_ACTIVE", "human": map[string]any{}}}
		default:
			w.WriteHeader(404)
			body = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	issuer = server.URL
	base.Config["issuer"] = issuer
	base.Config["transportProfile"] = "loopback"
	base.Config["openfga"].(map[string]any)["url"] = issuer
	base.Manifest["clients"].(map[string]any)["web"].(map[string]any)["transportProfile"] = "loopback"
	if len(environment) > 0 {
		base.Config["environment"] = environment[0]
	}
	h.config, _ = json.Marshal(base.Config)
	h.manifest, _ = json.Marshal(base.Manifest)
	deployment, e := configuration.Parse(h.config, h.manifest)
	if e != nil {
		t.Fatal(e)
	}
	model = deployment.Artifact().Model
	model["id"] = "model-1"
	if failure == "model" {
		model["id"] = "wrong"
	}
	for k, v := range map[string]string{"issuer": issuer, "jwks_uri": issuer + "/oauth/v2/keys", "authorization_endpoint": issuer + "/oauth/v2/authorize", "token_endpoint": issuer + "/oauth/v2/token", "introspection_endpoint": issuer + "/oauth/v2/introspect", "revocation_endpoint": issuer + "/oauth/v2/revoke"} {
		discovery.Metadata[k] = v
	}
	if failure == "issuer" {
		discovery.Metadata["issuer"] = issuer + "/"
	}
	if failure == "introspection" {
		discovery.Metadata["introspection_endpoint"] = issuer + "/unexpected"
	}
	signed := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": issuer, "aud": "ledger-api", "sub": "runtime-user", "iat": time.Now().Unix(), "exp": expiresAt})
	signed.Header["kid"] = "runtime"
	h.token, e = signed.SignedString(key)
	if e != nil {
		t.Fatal(e)
	}
	h.options = heimdall.Options{PolicyVersion: "p1", Credentials: heimdall.Credentials{ClientSecret: func(context.Context) (string, error) { return "secret", nil }, OpenFGAKey: func(context.Context) (string, error) {
		if failure == "credential" {
			return "", errors.New("secret")
		}
		return "key", nil
	}}, Storage: heimdall.RuntimeStorage{ProductID: "ledger", Environment: "production", Ready: func(context.Context) error {
		if failure == "storage" {
			return errors.New("secret")
		}
		return nil
	}, Identity: heimdall.IdentityGate{Check: func(context.Context, heimdall.Identity) (bool, error) { return false, nil }, Invalidate: func(context.Context, heimdall.IdentityNegative) error { return nil }}, AppendAudit: func(_ context.Context, event map[string]any) (heimdall.AuditReceipt, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.events = append(h.events, event)
		digest, e := heimdall.AuditEventDigest(event)
		return heimdall.AuditReceipt{ProductID: event["productId"].(string), Environment: event["environment"].(string), EventID: event["eventId"].(string), RecordedAt: int64(event["recordedAt"].(float64)), PayloadDigest: digest}, e
	}}}
	if failure == "scope" {
		h.options.Storage.ProductID = "support"
	}
	if failure == "policy" {
		h.options.PolicyVersion = "bad\n"
	}
	if failure == "missing" {
		h.options.Storage.Ready = nil
	}
	return h
}
func TestRuntimeStartup(t *testing.T) {
	raw, e := os.ReadFile("../../contracts/fixtures/runtime-startup.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct{ Name, Failure, Expected string }
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("cases")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			h := setupRuntime(t, c.Failure)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.Failure == "cancelled" {
				cancel()
			}
			client, e := heimdall.New(ctx, h.config, h.manifest, h.options)
			if c.Expected == "ready" {
				if e != nil || client == nil {
					t.Fatal(e)
				}
			} else if !errors.Is(e, contract.NewError(contract.Code(c.Expected), "")) {
				t.Fatal(e)
			}
			if len(h.events) != 0 {
				t.Fatal("startup emitted authentication")
			}
		})
	}
}
func TestPublicTokens(t *testing.T) {
	h := setupRuntime(t, "")
	client, e := heimdall.New(context.Background(), h.config, h.manifest, h.options)
	if e != nil {
		t.Fatal(e)
	}
	h.options.Storage.AppendAudit = nil
	h.options.Credentials.ClientSecret = nil
	p, e := client.Tokens().Verify(context.Background(), h.token)
	if e != nil || p.Identity().Subject != "runtime-user" || p.Kind() != "human" {
		t.Fatal(p, e)
	}
	if _, e = client.Tokens().Verify(context.Background(), "invalid"); !errors.Is(e, heimdall.ErrUnauthenticated) {
		t.Fatal(e)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.events) != 2 || h.events[0]["outcome"] != "succeeded" || h.events[1]["outcome"] != "deny" {
		t.Fatal("audit outcomes")
	}
	var zero heimdall.Client
	if _, e = zero.Tokens().Verify(context.Background(), h.token); !errors.Is(e, heimdall.ErrInvalidArgument) {
		t.Fatal(e)
	}
}
func TestRuntimeDeadline(t *testing.T) {
	h := setupRuntime(t, "")
	release := make(chan struct{})
	defer close(release)
	h.options.Storage.Ready = func(context.Context) error { <-release; return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := heimdall.New(ctx, h.config, h.manifest, h.options); !errors.Is(e, heimdall.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	h.options.Storage.AppendAudit = nil
	if _, e := heimdall.New(context.Background(), h.config, h.manifest, h.options); !errors.Is(e, heimdall.ErrInvalidArgument) {
		t.Fatal(e)
	}
}
func TestPublicAuditFailure(t *testing.T) {
	h := setupRuntime(t, "")
	h.options.Storage.AppendAudit = func(context.Context, map[string]any) (heimdall.AuditReceipt, error) {
		return heimdall.AuditReceipt{}, errors.New("sensitive store error")
	}
	client, e := heimdall.New(context.Background(), h.config, h.manifest, h.options)
	if e != nil {
		t.Fatal(e)
	}
	if p, e := client.Tokens().Verify(context.Background(), h.token); p != nil || !errors.Is(e, heimdall.ErrAuditUnavailable) {
		t.Fatal(p, e)
	}
}

func TestPostgresFactory(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://unused")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, err := os.ReadFile("../../contracts/fixtures/postgres-runtime.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, ProductID, Environment string
		AuditSinks                   []string
		Valid                        bool
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixture")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			storage, err := postgres.NewStorage(db, postgres.StorageOptions{ProductID: c.ProductID, Environment: c.Environment, AuditSinks: c.AuditSinks})
			if c.Valid {
				if err != nil || storage.ProductID != c.ProductID {
					t.Fatal(err)
				}
			} else if !errors.Is(err, heimdall.ErrInvalidArgument) {
				t.Fatal(err)
			}
		})
	}
	storage, err := postgres.NewStorage(db, postgres.StorageOptions{ProductID: "ledger", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.Ready(nil); !errors.Is(err, heimdall.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if _, err = storage.AppendAudit(nil, nil); !errors.Is(err, heimdall.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if _, err = storage.Identity.Check(nil, heimdall.Identity{}); !errors.Is(err, heimdall.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if err = storage.Identity.Invalidate(nil, heimdall.IdentityNegative{}); !errors.Is(err, heimdall.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if db.Stats().OpenConnections != 0 {
		t.Fatal("factory opened connection")
	}
}

type workspaceScan func(context.Context, heimdall.ScanOptions) (heimdall.CandidatePage, error)

func (scan workspaceScan) Scan(ctx context.Context, options heimdall.ScanOptions) (heimdall.CandidatePage, error) {
	return scan(ctx, options)
}

func TestPublicPostgresRuntime(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("public PostgreSQL runtime")
	}
	h := setupRuntime(t, "", "public-go")
	db, err := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_public_runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sinks := []string{"siem"}
	storage, err := postgres.NewStorage(db, postgres.StorageOptions{ProductID: "ledger", Environment: "public-go", AuditSinks: sinks,
		Sessions: &postgres.SessionStorageOptions{KeyUsageDB: db, ActiveKeyID: "k1", Keys: map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sinks[0] = "changed"
	h.options.Storage = storage
	ctx := context.Background()
	client, err := heimdall.New(ctx, h.config, h.manifest, h.options)
	if err != nil {
		t.Fatal(err)
	}
	options := heimdall.SessionOptions{CallbackURL: "https://other.example/callback", ReturnTo: "/workspaces", ZitadelAPIAudience: "api"}
	if _, err := client.NewSessions(ctx, options); !errors.Is(err, heimdall.ErrInvalidArgument) {
		t.Fatal(err)
	}
	options.CallbackURL = "https://ledger.example.com/auth/callback"
	sessions, err := client.NewSessions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if err = sessions.Start(recorder, httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/start", nil)); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 303 || !strings.HasPrefix(recorder.Header().Get("Set-Cookie"), "__Host-heimdall-") {
		t.Fatal("invalid login response")
	}
	var loginCount int
	if err = db.QueryRow("SELECT count(*) FROM heimdall.records WHERE environment='public-go' AND kind='login'").Scan(&loginCount); err != nil || loginCount != 1 {
		t.Fatal(loginCount, err)
	}
	actor, err := client.Tokens().Verify(ctx, h.token)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := client.Tenant("acme").Can(ctx, actor, "invoice.approve"); err != nil || !allowed {
		t.Fatal(allowed, err)
	}
	if err := client.Tenant("globex").Require(ctx, actor, "invoice.approve"); !errors.Is(err, heimdall.ErrForbidden) {
		t.Fatal(err)
	}
	if allowed, err := client.Tenant("outage").Can(ctx, actor, "invoice.approve"); allowed || !errors.Is(err, heimdall.ErrUnavailable) {
		t.Fatal(allowed, err)
	}
	if allowed, err := client.Tenant("acme").Can(ctx, &heimdall.Principal{}, "invoice.approve"); allowed || !errors.Is(err, heimdall.ErrUnauthenticated) {
		t.Fatal(allowed, err)
	}
	if _, err = client.Tokens().Verify(ctx, "invalid"); !errors.Is(err, heimdall.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if roles, err := client.Tenant("acme").Roles(ctx, actor); err != nil || !slices.Equal(roles, []string{"approver"}) {
		t.Fatal(roles, err)
	}
	if roles, err := client.Tenant("globex").Roles(ctx, actor); err != nil || len(roles) != 0 {
		t.Fatal(roles, err)
	}
	if roles, err := client.Tenant("outage").Roles(ctx, actor); !errors.Is(err, heimdall.ErrUnavailable) || roles != nil {
		t.Fatal(roles, err)
	}
	rows, err := db.Query("SELECT event->>'payload' FROM heimdall.audit_events WHERE environment='public-go'")
	if err != nil {
		t.Fatal(err)
	}
	var parent string
	var children []string
	for rows.Next() {
		var encoded string
		var event struct{ TenantID, Action, Outcome, DecisionID, ParentDecisionID string }
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		payload, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || json.Unmarshal(payload, &event) != nil {
			rows.Close()
			t.Fatal("invalid audit envelope")
		}
		if event.TenantID != "acme" {
			continue
		}
		if event.Action == "roles.list" && event.Outcome == "requested" {
			parent = event.DecisionID
		}
		if event.ParentDecisionID != "" {
			children = append(children, event.ParentDecisionID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if parent == "" || len(children) != 8 {
		t.Fatal("missing linked role decisions")
	}
	for _, child := range children {
		if child != parent {
			t.Fatal("incorrect parent decision ID")
		}
	}
	discoveryOptions := heimdall.DiscoveryOptions{
		ScanBudget: 1, ActiveKeyID: "cursor", Keys: map[string][]byte{"cursor": bytes.Repeat([]byte{4}, 32)},
		Source: workspaceScan(func(_ context.Context, scan heimdall.ScanOptions) (heimdall.CandidatePage, error) {
			id := "globex"
			if scan.After == "globex" {
				id = "acme"
			}
			return heimdall.CandidatePage{Items: []heimdall.Candidate{{Workspace: heimdall.Workspace{ID: id, Name: "Shared name"}, Position: id}}, Exhausted: id != "globex"}, nil
		}),
	}
	discovery, err := client.NewWorkspaceDiscovery(discoveryOptions)
	if err != nil {
		t.Fatal(err)
	}
	sparse, err := discovery.List(ctx, actor, heimdall.WorkspaceListOptions{})
	if err != nil || len(sparse.Items) != 0 || !strings.HasPrefix(sparse.NextCursor, "hc1.") {
		t.Fatal("expected empty page with cursor", err)
	}
	page, err := discovery.List(ctx, actor, heimdall.WorkspaceListOptions{Cursor: sparse.NextCursor})
	if err != nil || !slices.Equal(page.Items, []heimdall.Workspace{{ID: "acme", Name: "Shared name"}}) || page.NextCursor != "" {
		t.Fatal("expected allowed workspace", err)
	}
	if _, err := discovery.List(ctx, actor, heimdall.WorkspaceListOptions{Cursor: sparse.NextCursor + "x"}); !errors.Is(err, heimdall.ErrInvalidArgument) {
		t.Fatal(err)
	}
	discoveryOptions.Source = workspaceScan(func(context.Context, heimdall.ScanOptions) (heimdall.CandidatePage, error) {
		return heimdall.CandidatePage{Items: []heimdall.Candidate{{Workspace: heimdall.Workspace{ID: "outage", Name: "Unavailable"}, Position: "outage"}}, Exhausted: true}, nil
	})
	unavailable, err := client.NewWorkspaceDiscovery(discoveryOptions)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := unavailable.List(ctx, actor, heimdall.WorkspaceListOptions{}); !errors.Is(err, heimdall.ErrUnavailable) || page.NextCursor != "" || len(page.Items) != 0 {
		t.Fatal("expected failed page", err)
	}
	err = storage.Identity.Invalidate(ctx, heimdall.IdentityNegative{Identity: actor.Identity(), Context: "bearer", TokenDigest: strings.Repeat("a", 64), Reason: "identity_inactive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Tokens().Verify(ctx, h.token); !errors.Is(err, heimdall.ErrUnauthenticated) {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE product_id='ledger' AND environment='public-go'").Scan(&count); err != nil || count != 45 {
		t.Fatal(count, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='public-go' AND sink_id='siem'").Scan(&count); err != nil || count != 45 {
		t.Fatal(count, err)
	}
	if _, err = storage.AppendAudit(ctx, map[string]any{}); !errors.Is(err, heimdall.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = heimdall.New(ctx, h.config, h.manifest, h.options); !errors.Is(err, heimdall.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestPublicManagedRuntime(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("managed PostgreSQL runtime")
	}
	h := setupRuntime(t, "", "managed-go")
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	db, err := sql.Open("postgres", dsn+"&options=-c%20role%3Dheimdall_public_runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h.options.Storage, err = postgres.NewManagedStorage(db, postgres.StorageOptions{ProductID: "ledger", Environment: "managed-go"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	client, err := heimdall.New(ctx, h.config, h.manifest, h.options)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := client.Tokens().Verify(ctx, h.token)
	if err != nil {
		t.Fatal(err)
	}
	identity := actor.Identity()
	principal := "user:" + base64.RawURLEncoding.EncodeToString([]byte(`["`+identity.Issuer+`","runtime-user"]`))
	if _, err = admin.Exec("INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES('ledger','managed-go','acme','Acme','active',false,1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec("INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at) VALUES('ledger','managed-go','acme',sha256(convert_to($1,'UTF8')),$1,1)", principal); err != nil {
		t.Fatal(err)
	}
	options := heimdall.DiscoveryOptions{ActiveKeyID: "cursor", Keys: map[string][]byte{"cursor": make([]byte, 32)}}
	discovery, err := client.NewWorkspaceDiscovery(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Source = workspaceScan(func(context.Context, heimdall.ScanOptions) (heimdall.CandidatePage, error) {
		return heimdall.CandidatePage{Exhausted: true}, nil
	})
	if _, err = client.NewWorkspaceDiscovery(options); !errors.Is(err, heimdall.ErrInvalidArgument) {
		t.Fatal(err)
	}
	tenant := client.Tenant("acme")
	if err := tenant.Require(ctx, actor, "invoice.approve"); err != nil {
		t.Fatal(err)
	}
	if roles, err := tenant.Roles(ctx, actor); err != nil || !slices.Equal(roles, []string{"approver"}) {
		t.Fatal(roles, err)
	}
	if page, err := discovery.List(ctx, actor, heimdall.WorkspaceListOptions{}); err != nil || !slices.Equal(page.Items, []heimdall.Workspace{{ID: "acme", Name: "Acme"}}) {
		t.Fatal(page, err)
	}
	count := h.checks.Load()
	if _, err = admin.Exec("UPDATE heimdall_managed.workspaces SET mutation_blocked=true WHERE environment='managed-go'"); err != nil {
		t.Fatal(err)
	}
	if err := tenant.Require(ctx, actor, "invoice.approve"); !errors.Is(err, heimdall.ErrForbidden) {
		t.Fatal(err)
	}
	if roles, err := tenant.Roles(ctx, actor); err != nil || len(roles) != 0 {
		t.Fatal(roles, err)
	}
	if page, err := discovery.List(ctx, actor, heimdall.WorkspaceListOptions{}); err != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	if h.checks.Load() != count {
		t.Fatal("blocked workspace called OpenFGA")
	}
}

type loginStartStore struct {
	heimdall.SessionStore
	create func(string, []byte) (string, error)
}

func (s loginStartStore) Create(_ context.Context, key string, value []byte, _ int64) (string, error) {
	return s.create(key, value)
}

func TestPublicCustomSessionStore(t *testing.T) {
	h := setupRuntime(t, "")
	var reserved bool
	h.options.SessionStorage = &heimdall.SessionStoreOptions{
		ActiveKeyID: "k1", Keys: map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)},
		ReserveKeyUsage: func(context.Context, string) error { reserved = true; return nil },
		Store: loginStartStore{create: func(key string, value []byte) (string, error) {
			if !reserved || !strings.HasPrefix(key, "heimdall.v1|ledger|production|login|") ||
				!bytes.HasPrefix(value, []byte("hr1.k1.")) {
				return "", errors.New("missing scope, encryption or usage reservation")
			}
			return "1", nil
		}},
	}
	ctx := context.Background()
	client, err := heimdall.New(ctx, h.config, h.manifest, h.options)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := client.NewSessions(ctx, heimdall.SessionOptions{
		CallbackURL: "https://ledger.example.com/auth/callback", ReturnTo: "/workspaces", ZitadelAPIAudience: "zitadel",
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err = sessions.Start(w, httptest.NewRequest("GET", "https://ledger.example.com/auth/start", nil)); err != nil || w.Code != 303 {
		t.Fatal(w.Code, err)
	}
}

func TestPublicManagedOnboarding(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("managed PostgreSQL runtime")
	}
	h := setupRuntime(t, "", "onboarding-go")
	db, err := sql.Open("postgres", dsn+"&options=-c%20role%3Dheimdall_public_runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	h.options.Storage, err = postgres.NewManagedStorage(db, postgres.StorageOptions{ProductID: "ledger", Environment: "onboarding-go", AuditSinks: []string{"siem"}})
	if err != nil {
		t.Fatal(err)
	}
	var permit atomic.Bool
	var approvals atomic.Int64
	h.options.AuthorizeWorkspaceCreation = func(context.Context, *heimdall.Principal, string) (bool, error) {
		approvals.Add(1)
		return permit.Load(), nil
	}
	ctx := context.Background()
	client, err := heimdall.New(ctx, h.config, h.manifest, h.options)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := client.Tokens().Verify(ctx, h.token)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Name, IdempotencyKey string
		Cases                []struct {
			Fault, Status, Reason string
			Writes                int64
		}
	}
	raw, err := os.ReadFile("../../contracts/fixtures/workspace-onboarding.json")
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("onboarding fixture")
	}
	api := client.Onboarding()
	input := heimdall.WorkspaceCreateInput{Name: fixture.Name, IdempotencyKey: fixture.IdempotencyKey}
	if _, err := api.Create(ctx, actor, input); !errors.Is(err, heimdall.ErrForbidden) || h.writes.Load() != 0 {
		t.Fatal("creation policy denial", err)
	}
	permit.Store(true)
	other, err := heimdall.New(ctx, h.config, h.manifest, h.options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Onboarding().Create(ctx, actor, input); !errors.Is(err, heimdall.ErrUnauthenticated) {
		t.Fatal("accepted an actor from a different runtime", err)
	}
	restartedActor, err := other.Tokens().Verify(ctx, h.token)
	if err != nil {
		t.Fatal(err)
	}
	var active []string
	for _, item := range fixture.Cases {
		input.IdempotencyKey = fixture.IdempotencyKey + "-" + item.Fault
		before := h.writes.Load()
		h.providerFault.Store(item.Fault)
		if item.Fault == "activation_failure" {
			_, err := admin.Exec(`CREATE FUNCTION public.fail_onboarding_go() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN IF NEW.environment='onboarding-go' AND NEW.lifecycle='active' THEN
			  RAISE EXCEPTION 'test activation failure'; END IF; RETURN NEW; END $$;
			CREATE TRIGGER fail_onboarding_go BEFORE UPDATE ON heimdall_managed.workspaces
			FOR EACH ROW EXECUTE FUNCTION public.fail_onboarding_go()`)
			if err != nil {
				t.Fatal(err)
			}
			_, createErr := api.Create(ctx, actor, input)
			_, cleanupErr := admin.Exec("DROP TRIGGER fail_onboarding_go ON heimdall_managed.workspaces; DROP FUNCTION public.fail_onboarding_go()")
			if createErr == nil || cleanupErr != nil {
				t.Fatal("activation failure", createErr, cleanupErr)
			}
		}
		result, err := api.Create(ctx, actor, input)
		if err != nil || result.Status != item.Status || result.Reason != item.Reason || h.writes.Load()-before != item.Writes {
			t.Fatal(item.Fault, result, err, h.writes.Load()-before)
		}
		permit.Store(false)
		count := approvals.Load()
		if again, err := api.Create(ctx, actor, input); err != nil || again != result || approvals.Load() != count {
			t.Fatal("idempotent status", again, err)
		}
		if _, err := api.Create(ctx, actor, heimdall.WorkspaceCreateInput{Name: "Changed", IdempotencyKey: input.IdempotencyKey}); !errors.Is(err, heimdall.ErrConflict) {
			t.Fatal("changed input", err)
		}
		if got, err := api.Get(ctx, actor, result.OperationID); err != nil || got != result {
			t.Fatal("operation status", got, err)
		}
		var state string
		var generation int64
		if err := admin.QueryRow("SELECT state,generation FROM heimdall_managed.operations WHERE environment='onboarding-go' AND operation_id=$1", result.OperationID).Scan(&state, &generation); err != nil {
			t.Fatal(err)
		}
		if item.Fault == "activation_failure" && state != "confirmed" {
			t.Fatal(state)
		}
		if item.Fault == "lost_response" || item.Fault == "lost_before_apply" {
			allowed, err := h.options.Storage.Managed.Allows(ctx, result.WorkspaceID)
			if state != "uncertain" || allowed || err != nil {
				t.Fatal("uncertain gate", state, allowed, err)
			}
		}
		h.providerFault.Store("none")
		resumed, err := other.Onboarding().Resume(ctx, restartedActor, result.OperationID)
		if err != nil {
			t.Fatal(item.Fault, err)
		}
		if item.Fault == "lost_response" || item.Fault == "lost_before_apply" {
			if resumed != result {
				t.Fatal("uncertain dispatch was resumed", resumed)
			}
			identity := actor.Identity()
			recovery := provisioning.WorkspaceRecoveryOptions{
				OperationID: result.OperationID, ExpectedGeneration: generation, PolicyVersion: "p1",
				InitiatingIssuer: identity.Issuer, InitiatingSubject: identity.Subject,
				RecoveryToken: "new-key", AuditSinks: []string{"siem"},
				VerifyQuiescence: func(context.Context, provisioning.WorkspaceRecoveryScope) (provisioning.WorkspaceRecoveryEvidence, error) {
					return provisioning.WorkspaceRecoveryEvidence{}, nil
				},
			}
			if _, err := provisioning.RecoverWorkspaceCreation(ctx, db, h.config, h.manifest, recovery); !errors.Is(err, heimdall.ErrForbidden) {
				t.Fatal("accepted missing quiescence proof", err)
			}
			recovery.VerifyQuiescence = func(_ context.Context, scope provisioning.WorkspaceRecoveryScope) (provisioning.WorkspaceRecoveryEvidence, error) {
				if scope.WorkspaceID != result.WorkspaceID || scope.Generation != generation {
					return provisioning.WorkspaceRecoveryEvidence{}, heimdall.ErrForbidden
				}
				return provisioning.WorkspaceRecoveryEvidence{OperatorID: "operator", EvidenceID: "drain-record"}, nil
			}
			recovered, err := provisioning.RecoverWorkspaceCreation(ctx, db, h.config, h.manifest, recovery)
			if err != nil || recovered.Status != "active" || recovered.WorkspaceID != result.WorkspaceID {
				t.Fatal("recovery", recovered, err)
			}
			if _, err := provisioning.RecoverWorkspaceCreation(ctx, db, h.config, h.manifest, recovery); !errors.Is(err, heimdall.ErrConflict) {
				t.Fatal("stale recovery", err)
			}
			active = append(active, recovered.WorkspaceID)
		} else {
			if resumed.Status != "active" {
				t.Fatal(item.Fault, resumed)
			}
			active = append(active, resumed.WorkspaceID)
		}
		writes := item.Writes
		if item.Fault == "before_dispatch" || item.Fault == "lost_before_apply" {
			writes++
		}
		if h.writes.Load()-before != writes {
			t.Fatal("unexpected remote replay", item.Fault)
		}
		permit.Store(true)
	}
	race := heimdall.WorkspaceCreateInput{Name: "Concurrent", IdempotencyKey: "concurrent-create"}
	beforeRace := h.writes.Load()
	type concurrentResult struct {
		status heimdall.WorkspaceOnboardingStatus
		err    error
	}
	out := make(chan concurrentResult, 4)
	for range 4 {
		go func() {
			status, err := api.Create(ctx, actor, race)
			out <- concurrentResult{status, err}
		}()
	}
	var opID, workspaceID string
	for range 4 {
		result := <-out
		if result.err != nil {
			t.Fatal(result.err)
		}
		if opID == "" {
			opID, workspaceID = result.status.OperationID, result.status.WorkspaceID
		}
		if opID != result.status.OperationID || workspaceID != result.status.WorkspaceID {
			t.Fatal("concurrent requests did not converge")
		}
	}
	winner, err := api.Get(ctx, actor, opID)
	if err != nil || winner.Status != "active" || h.writes.Load()-beforeRace != 1 {
		t.Fatal("concurrent dispatch", winner, err)
	}
	active = append(active, workspaceID)
	discovery, err := client.NewWorkspaceDiscovery(heimdall.DiscoveryOptions{ActiveKeyID: "cursor", Keys: map[string][]byte{"cursor": make([]byte, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := discovery.List(ctx, actor, heimdall.WorkspaceListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var discovered []string
	for _, workspace := range page.Items {
		discovered = append(discovered, workspace.ID)
	}
	if !slices.Equal(discovered, active) {
		t.Fatal("candidate state", discovered, active)
	}
	var recoveryCount int
	if err := admin.QueryRow(`SELECT count(*) FROM heimdall.audit_events WHERE environment='onboarding-go'
		AND convert_from(decode(translate(event->>'payload','-_','+/') ||
		repeat('=', (4-length(event->>'payload')%4)%4), 'base64'),'UTF8')::jsonb
		@> '{"requestId":"drain-record","actor":{"kind":"system","id":"operator"}}'`).Scan(&recoveryCount); err != nil || recoveryCount != 6 {
		t.Fatal("recovery audit", recoveryCount, err)
	}
	var count int
	if err := admin.QueryRow("SELECT count(*) FROM heimdall.audit_deliveries WHERE environment='onboarding-go' AND sink_id='siem'").Scan(&count); err != nil || count <= 20 {
		t.Fatal("audit routing", count, err)
	}
}
