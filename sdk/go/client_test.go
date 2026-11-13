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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type runtimeHarness struct {
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
		case "/stores/store-1/check":
			var check struct {
				Model       string `json:"authorization_model_id"`
				Consistency string
				Tuple       map[string]string `json:"tuple_key"`
			}
			user := "user:" + base64.RawURLEncoding.EncodeToString([]byte(`["`+issuer+`","runtime-user"]`))
			if json.NewDecoder(r.Body).Decode(&check) != nil || check.Model != "model-1" ||
				check.Consistency != "HIGHER_CONSISTENCY" || check.Tuple["user"] != user || check.Tuple["relation"] != "p_0" {
				w.WriteHeader(400)
				return
			}
			if check.Tuple["object"] == "workspace:outage" {
				w.WriteHeader(503)
				return
			}
			body = map[string]any{"allowed": check.Tuple["object"] == "workspace:acme"}
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
	err = storage.Identity.Invalidate(ctx, heimdall.IdentityNegative{Identity: actor.Identity(), Context: "bearer", TokenDigest: strings.Repeat("a", 64), Reason: "identity_inactive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Tokens().Verify(ctx, h.token); !errors.Is(err, heimdall.ErrUnauthenticated) {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE product_id='ledger' AND environment='public-go'").Scan(&count); err != nil || count != 9 {
		t.Fatal(count, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='public-go' AND sink_id='siem'").Scan(&count); err != nil || count != 9 {
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
