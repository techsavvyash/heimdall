package browserlogin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type afterSessionVerify struct {
	sessionAuth
	action func()
}

func (a afterSessionVerify) Verify(ctx context.Context, token, scope string) (*identity.Principal, error) {
	actor, err := a.sessionAuth.Verify(ctx, token, scope)
	if err == nil {
		a.action()
	}
	return actor, err
}

type unknownSessionWrite struct{ *postgres.SessionRecords }

func (unknownSessionWrite) CreateSession(context.Context, string, []byte, int64) (*postgres.Record, error) {
	return nil, contract.ErrOutcomeUnknown
}

func TestBrowserLoginHTTP(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live PostgreSQL integration")
	}
	read := func(name string, dest any) {
		t.Helper()
		raw, err := os.ReadFile("../../../../contracts/fixtures/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, dest); err != nil {
			t.Fatal(err)
		}
	}
	var f struct {
		ProductID, Environment, ReturnToKey, RedirectURI, ClientID, Audience, Code string
		Cases                                                                      []string
	}
	read("browser-login-http", &f)
	f.Environment += "-go"
	var discovery struct {
		Issuer   string
		Metadata map[string]any
	}
	read("oidc-discovery", &discovery)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	jwks := map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "login-http", "n": encode(key.N.Bytes()), "e": "AQAB"}}}
	var pending sync.Map
	var exchanges atomic.Int64
	exp := time.Now().Unix() + 300
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := "http://" + r.Host
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			metadata := map[string]any{}
			for name, value := range discovery.Metadata {
				if endpoint, ok := value.(string); ok && strings.HasPrefix(endpoint, discovery.Issuer) {
					value = issuer + strings.TrimPrefix(endpoint, discovery.Issuer)
				}
				metadata[name] = value
			}
			_ = json.NewEncoder(w).Encode(metadata)
		case "/oauth/v2/keys":
			_ = json.NewEncoder(w).Encode(jwks)
		case "/oauth/v2/introspect":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "iss": issuer, "sub": "alice", "aud": []string{f.Audience}, "exp": exp})
		case "/v2/users/alice":
			_, _ = w.Write([]byte(`{"user":{"userId":"alice","state":"USER_STATE_ACTIVE","human":{}}}`))
		case "/oauth/v2/token":
			exchanges.Add(1)
			if r.ParseForm() != nil || r.Form.Get("redirect_uri") != f.RedirectURI {
				t.Error("incorrect exchange request")
			}
			challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			nonce, ok := pending.Load(encode(challenge[:]))
			if !ok {
				t.Error("missing PKCE challenge")
			}
			sign := func(audience string, claims jwt.MapClaims) string {
				claims["iss"], claims["sub"], claims["aud"] = issuer, "alice", audience
				claims["iat"], claims["exp"] = time.Now().Unix(), exp
				token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
				token.Header["kid"] = "login-http"
				raw, err := token.SignedString(key)
				if err != nil {
					t.Error(err)
				}
				return raw
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token_type": "Bearer", "access_token": sign(f.Audience, jwt.MapClaims{}),
				"id_token": sign(f.ClientID, jwt.MapClaims{"nonce": nonce}),
			})
		default:
			t.Error("unexpected OIDC request")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, err := contract.NewRecordCipher("k1", map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewRecords(db, db, cipher, f.ProductID, f.Environment, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(context.Background(), identity.BrowserOIDCConfig{
		Issuer: server.URL, Profile: "loopback", ClientID: f.ClientID, ClientSecret: "fixture", Audience: f.Audience, RedirectURI: f.RedirectURI,
	}, postgres.NewBrowserLoginRecords(store, nil), f.ProductID, f.Environment, f.ReturnToKey)
	if err != nil {
		t.Fatal(err)
	}
	type flow struct{ state, cookie string }
	start := func() flow {
		t.Helper()
		w := httptest.NewRecorder()
		if err := h.Start(w, httptest.NewRequest(http.MethodGet, "https://ledger.example.test/auth/start", nil)); err != nil {
			t.Fatal(err)
		}
		res := w.Result()
		defer res.Body.Close()
		location, err := res.Location()
		if err != nil {
			t.Fatal(err)
		}
		q := location.Query()
		cookies := res.Cookies()
		if res.StatusCode != 303 || res.Header.Get("Cache-Control") != "no-store" ||
			location.Host != strings.TrimPrefix(server.URL, "http://") || location.Path != "/oauth/v2/authorize" ||
			q.Get("code_challenge_method") != "S256" ||
			!strings.Contains(q.Get("scope"), "urn:zitadel:iam:org:project:id:"+f.Audience+":aud") ||
			len(cookies) != 1 || !strings.HasPrefix(cookies[0].Name, "__Host-heimdall-") ||
			cookies[0].Path != "/" || cookies[0].Domain != "" || cookies[0].MaxAge != 600 ||
			!cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatal("incorrect authorization response")
		}
		pending.Store(q.Get("code_challenge"), q.Get("nonce"))
		return flow{q.Get("state"), cookies[0].Name + "=" + cookies[0].Value}
	}
	a, b := start(), start()
	callback := func(fx flow, method, target, extra, cookie string) (*CallbackResult, error) {
		query := url.Values{"code": {f.Code}, "state": {fx.state}}
		r := httptest.NewRequest(method, target+"?"+query.Encode()+extra, nil)
		r.Header.Set("Cookie", cookie)
		return h.Callback(r)
	}
	for _, name := range f.Cases {
		t.Run(name, func(t *testing.T) {
			method, target, extra, cookie := http.MethodGet, f.RedirectURI, "", a.cookie
			switch name {
			case "missing cookie":
				cookie = ""
			case "other tab cookie":
				cookie = b.cookie
			case "duplicate cookie":
				cookie += "; " + cookie
			case "duplicate state":
				extra = "&state=" + a.state
			case "wrong issuer":
				extra = "&iss=https%3A%2F%2Fwrong.example.test"
			case "wrong host":
				target = "https://other.example.test/auth/callback"
			case "wrong path":
				target += "/other"
			case "wrong method":
				method = http.MethodPost
			default:
				t.Fatal("unknown fixture")
			}
			if result, err := callback(a, method, target, extra, cookie); result != nil || !errors.Is(err, contract.ErrUnauthenticated) {
				t.Fatal("invalid callback accepted", err)
			}
		})
	}
	if exchanges.Load() != 0 {
		t.Fatal("invalid callback reached token endpoint")
	}
	var wg sync.WaitGroup
	var wins atomic.Int64
	for _, fx := range []flow{a, a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := callback(fx, http.MethodGet, f.RedirectURI, "", fx.cookie)
			if errors.Is(err, contract.ErrUnauthenticated) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			if result.Tokens.Identity.Subject() != "alice" || result.ReturnToKey != f.ReturnToKey ||
				result.ClearBindingCookie.MaxAge != -1 || !strings.HasPrefix(fx.cookie, result.ClearBindingCookie.Name+"=") {
				t.Error("incorrect callback result")
			}
			wins.Add(1)
		}()
	}
	wg.Wait()
	if wins.Load() != 2 || exchanges.Load() != 2 {
		t.Fatal("expected one exchange per flow")
	}
	if result, err := callback(a, http.MethodGet, f.RedirectURI, "", a.cookie); result != nil || !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("replay accepted", err)
	}
	sessionsStore, err := postgres.NewSessionRecords(db, db, cipher, f.ProductID, f.Environment, func(value []byte) (postgres.SessionPrincipal, error) {
		var principal postgres.SessionPrincipal
		err := json.Unmarshal(value, &principal)
		return principal, err
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := sessionsStore.IdentityGate(f.ProductID, f.Environment)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := postgres.NewAudit(db, f.ProductID, f.Environment, []string{"siem"}, contract.ValidateAuditShape)
	if err != nil {
		t.Fatal(err)
	}
	var failAudit atomic.Bool
	failAudit.Store(true)
	auth, err := auditflow.NewAuthentication(identity.StatusConfig{
		Config:    identity.Config{Issuer: server.URL, Audience: f.Audience, JWKSURI: server.URL + "/oauth/v2/keys", Profile: "loopback"},
		ProductID: f.ProductID, Environment: f.Environment, PolicyVersion: "p1", ClientID: f.ClientID,
	}, func(context.Context) (string, error) { return "fixture", nil }, gate, func(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
		if failAudit.Load() {
			return contract.AuditReceipt{}, contract.ErrAuditUnavailable
		}
		return audit.Append(ctx, event)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessions(h, auth, sessionsStore, "/workspaces", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	finish := func(sessions *Sessions, fx flow) (*http.Response, error) {
		query := url.Values{"code": {f.Code}, "state": {fx.state}}
		r := httptest.NewRequest(http.MethodGet, f.RedirectURI+"?"+query.Encode(), nil)
		r.Header.Set("Cookie", fx.cookie)
		w := httptest.NewRecorder()
		err := sessions.Callback(w, r)
		return w.Result(), err
	}
	res, err := finish(sessions, start())
	res.Body.Close()
	if !errors.Is(err, contract.ErrAuditUnavailable) || len(res.Cookies()) != 0 {
		t.Fatal("audit failure released a cookie", err)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind='session'", f.ProductID, f.Environment).Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure created a session", err)
	}
	failAudit.Store(false)
	uncertain, err := NewSessions(h, auth, unknownSessionWrite{sessionsStore}, "/workspaces", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = finish(uncertain, start())
	res.Body.Close()
	if !errors.Is(err, contract.ErrOutcomeUnknown) || len(res.Cookies()) != 0 {
		t.Fatal("unconfirmed session write released a cookie", err)
	}
	res, err = finish(sessions, start())
	res.Body.Close()
	if err != nil || res.StatusCode != 303 || res.Header.Get("Location") != "/workspaces" {
		t.Fatal("callback failed", err)
	}
	cookies := res.Cookies()
	if len(cookies) != 2 || cookies[0].MaxAge != -1 || !strings.HasSuffix(cookies[1].Name, "-session") || !cookies[1].Secure || !cookies[1].HttpOnly {
		t.Fatal("invalid callback cookies")
	}
	opaque := cookies[1].Value
	digest := sha256.Sum256([]byte(opaque))
	stored, err := sessionsStore.GetSession(context.Background(), encode(digest[:]))
	if err != nil || stored == nil {
		t.Fatal("session was not committed", err)
	}
	var payload map[string]any
	if err = json.Unmarshal(stored.Value, &payload); err != nil || payload["subject"] != "alice" || payload["policyVersion"] != "p1" || payload["state"] != "active" {
		t.Fatal("invalid session payload", err)
	}
	if err = db.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2", f.ProductID, f.Environment).Scan(&count); err != nil || count != 2 {
		t.Fatal("expected two committed authentication events", err)
	}
	sessionCookie := cookies[1].Name + "=" + opaque
	recordKey := encode(digest[:])
	protected := httptest.NewRequest(http.MethodGet, "https://ledger.example.test/workspaces", nil)
	protected.Header.Set("Cookie", sessionCookie)
	if actor, err := sessions.Authenticate(protected); err != nil || actor.Identity().Subject != "alice" {
		t.Fatal("session authentication failed", err)
	}
	touched, err := sessionsStore.GetSession(context.Background(), recordKey)
	if err != nil || touched == nil || touched.Revision == stored.Revision {
		t.Fatal("session activity did not advance", err)
	}
	logout := func(origin string) (*http.Response, error) {
		r := httptest.NewRequest(http.MethodPost, "https://ledger.example.test/auth/logout", nil)
		r.Header.Set("Cookie", sessionCookie)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		err := sessions.Logout(w, r)
		return w.Result(), err
	}
	res, err = logout("https://other.example.test")
	res.Body.Close()
	if !errors.Is(err, contract.ErrForbidden) || len(res.Cookies()) != 0 {
		t.Fatal("cross-origin logout accepted", err)
	}
	race, err := NewSessions(h, afterSessionVerify{auth, func() {
		res, err := logout("https://ledger.example.test")
		res.Body.Close()
		if err != nil || res.StatusCode != 204 || len(res.Cookies()) != 1 || res.Cookies()[0].MaxAge != -1 {
			t.Fatal("logout failed", err)
		}
	}}, sessionsStore, "/workspaces", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if actor, err := race.Authenticate(protected); actor != nil || !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("authentication survived logout race", err)
	}
	if current, err := sessionsStore.GetSession(context.Background(), recordKey); current != nil || err != nil {
		t.Fatal("logout did not delete session", err)
	}
	if actor, err := sessions.Authenticate(protected); actor != nil || !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("logged out cookie authenticated", err)
	}
	res, err = logout("https://ledger.example.test")
	res.Body.Close()
	if err != nil || res.StatusCode != 204 {
		t.Fatal("logout was not idempotent", err)
	}

}
