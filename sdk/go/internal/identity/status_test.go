package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type statusCase struct {
	Name, Expected, NegativeReason  string
	IntrospectionStatus, UserStatus int
	Introspection, User             json.RawMessage
}

func statusCases(t *testing.T) []statusCase {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/identity-status.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []statusCase
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixtures")
	}
	return cases
}

type statusHarness struct {
	v             *IdentityVerifier
	now           atomic.Int64
	blocked, fail atomic.Bool
	calls         atomic.Int32
	mu            sync.Mutex
	negatives     []Negative
	fixture       fixture
}

func statusSetup(t *testing.T, c *statusCase, freshness int64) *statusHarness {
	t.Helper()
	f := load(t)
	h := &statusHarness{fixture: f}
	h.now.Store(f.Now)
	gate := Gate{Check: func(context.Context, Identity) (bool, error) { return h.blocked.Load(), nil }, Invalidate: func(_ context.Context, n Negative) error {
		h.mu.Lock()
		h.negatives = append(h.negatives, n)
		h.mu.Unlock()
		if h.fail.Load() {
			return contract.ErrUnavailable
		}
		return nil
	}}
	v, e := NewIdentityVerifier(StatusConfig{Config: Config{f.Issuer, f.Audience, f.Issuer + "/keys", "production"}, ProductID: "ledger", Environment: "production", PolicyVersion: "p1", ClientID: "client", FreshnessMs: &freshness}, func(context.Context) (string, error) { return "s:e+cret", nil }, gate, func() time.Time { return time.UnixMilli(h.now.Load()) })
	if e != nil {
		t.Fatal(e)
	}
	h.v = v
	v.tokens.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/keys":
			return response(string(f.JWKS), 200), nil
		case "/oauth/v2/introspect":
			h.calls.Add(1)
			if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("client:s%3Ae%2Bcret")) {
				return nil, errors.New("bad auth header")
			}
			return response(string(c.Introspection), c.IntrospectionStatus), nil
		default:
			if r.URL.Path != "/v2/users/"+f.Subject || r.Header.Get("Authorization") != "Bearer "+f.token("valid") {
				return nil, errors.New("bad self request")
			}
			return response(string(c.User), c.UserStatus), nil
		}
	})
	return h
}
func TestStatusCases(t *testing.T) {
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			h := statusSetup(t, &c, 60000)
			p, e := h.v.Verify(context.Background(), h.fixture.token("valid"), "session:1")
			if c.Expected == "active" {
				if e != nil || !h.v.Owns(p) || p.Kind() != "human" || p.Identity().Subject != h.fixture.Subject || p.ValidatedAt() != h.fixture.Now {
					t.Fatal(p, e)
				}
				if h.v.Owns(&Principal{}) {
					t.Fatal("fabrication")
				}
			} else if !errors.Is(e, contract.NewError(contract.Code(c.Expected), "")) {
				t.Fatal(e)
			}
			if c.NegativeReason == "" {
				if len(h.negatives) != 0 {
					t.Fatal(h.negatives)
				}
			} else if len(h.negatives) != 1 || h.negatives[0].Reason != c.NegativeReason || h.negatives[0].Context != "session:1" {
				t.Fatal(h.negatives)
			}
		})
	}
}
func TestStatusFreshness(t *testing.T) {
	c := statusCases(t)[0]
	h := statusSetup(t, &c, 60000)
	ctx := context.Background()
	raw := h.fixture.token("valid")
	if _, e := h.v.Verify(ctx, raw, "a:1"); e != nil {
		t.Fatal(e)
	}
	h.now.Add(59999)
	if _, e := h.v.Verify(ctx, raw, "a:1"); e != nil {
		t.Fatal(e)
	}
	if h.calls.Load() != 1 {
		t.Fatal(h.calls.Load())
	}
	if _, e := h.v.Verify(ctx, raw, "b:1"); e != nil {
		t.Fatal(e)
	}
	if h.calls.Load() != 2 {
		t.Fatal(h.calls.Load())
	}
	c.IntrospectionStatus = 503
	h.now.Add(1)
	if _, e := h.v.Verify(ctx, raw, "a:1"); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if h.calls.Load() != 3 {
		t.Fatal(h.calls.Load())
	}
	if _, e := h.v.Verify(ctx, raw, "b:1"); e != nil {
		t.Fatal(e)
	}
	if h.calls.Load() != 3 {
		t.Fatal(h.calls.Load())
	}
}
func TestOnlineAndGate(t *testing.T) {
	c := statusCases(t)[0]
	h := statusSetup(t, &c, 0)
	for range 2 {
		if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), "bearer"); e != nil {
			t.Fatal(e)
		}
	}
	if h.calls.Load() != 2 {
		t.Fatal(h.calls.Load())
	}
	h.blocked.Store(true)
	if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), "bearer"); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if h.calls.Load() != 2 {
		t.Fatal(h.calls.Load())
	}
}
func TestNegativeClearsContexts(t *testing.T) {
	c := statusCases(t)[0]
	h := statusSetup(t, &c, 60000)
	for _, key := range []string{"a", "b"} {
		if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), key); e != nil {
			t.Fatal(e)
		}
	}
	c.Introspection = json.RawMessage(`{"active":false}`)
	for _, key := range []string{"c", "a"} {
		if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), key); !errors.Is(e, contract.ErrUnauthenticated) {
			t.Fatal(e)
		}
	}
	if h.calls.Load() != 4 {
		t.Fatal(h.calls.Load())
	}
	h.fail.Store(true)
	if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), "d"); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
}
func TestStatusLoaderDeadline(t *testing.T) {
	c := statusCases(t)[0]
	h := statusSetup(t, &c, 0)
	h.v.secret = func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := h.v.Verify(ctx, h.fixture.token("valid"), "bearer"); !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	if h.calls.Load() != 0 {
		t.Fatal(h.calls.Load())
	}
	other := statusSetup(t, &c, 0)
	other.v.secret = func(context.Context) (string, error) { return "", errors.New("secret-value") }
	if _, e := other.v.Verify(context.Background(), other.fixture.token("valid"), "bearer"); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
}

func TestStatusNegativeWinsRace(t *testing.T) {
	c := statusCases(t)[0]
	h := statusSetup(t, &c, 60000)
	var calls atomic.Int32
	held := make(chan struct{})
	started := make(chan struct{})
	h.v.tokens.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/keys" {
			return response(string(h.fixture.JWKS), 200), nil
		}
		if r.URL.Path == "/oauth/v2/introspect" {
			if calls.Add(1) == 1 {
				close(started)
				<-held
				return response(string(c.Introspection), 200), nil
			}
			return response(`{"active":false}`, 200), nil
		}
		return response(string(c.User), 200), nil
	})
	old := make(chan error, 1)
	go func() { _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), "old"); old <- e }()
	<-started
	if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), "new"); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal(e)
	}
	close(held)
	if e := <-old; !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), "old"); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
}
func TestStatusExpiryCap(t *testing.T) {
	c := statusCases(t)[0]
	f := load(t)
	var value map[string]any
	_ = json.Unmarshal(c.Introspection, &value)
	value["exp"] = f.Now/1000 + 1
	c.Introspection, _ = json.Marshal(value)
	h := statusSetup(t, &c, 60000)
	if _, e := h.v.Verify(context.Background(), f.token("valid"), "bearer"); e != nil {
		t.Fatal(e)
	}
	h.now.Add(1000)
	c.IntrospectionStatus = 503
	if _, e := h.v.Verify(context.Background(), f.token("valid"), "bearer"); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if h.calls.Load() != 2 {
		t.Fatal(h.calls.Load())
	}
}

func TestLiveStatus(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_STATUS_CASE")
	if raw == "" {
		t.Skip("live status")
	}
	var c struct{ Issuer, Audience, JWKSURI, ClientID, ClientSecret, Product, Token, Expected, Subject string }
	if json.Unmarshal([]byte(raw), &c) != nil {
		t.Fatal("live config")
	}
	freshness := int64(0)
	negatives := []string{}
	v, e := NewIdentityVerifier(StatusConfig{Config: Config{c.Issuer, c.Audience, c.JWKSURI, "production"}, ProductID: c.Product, Environment: "verification", PolicyVersion: "live-v1", ClientID: c.ClientID, FreshnessMs: &freshness}, func(context.Context) (string, error) { return c.ClientSecret, nil }, Gate{Check: func(context.Context, Identity) (bool, error) { return false, nil }, Invalidate: func(_ context.Context, n Negative) error { negatives = append(negatives, n.Reason); return nil }}, nil)
	if e != nil {
		t.Fatal(e)
	}
	p, e := v.Verify(context.Background(), c.Token, "live")
	if c.Expected == "human" {
		if e != nil || p.Identity().Subject != c.Subject || !v.Owns(p) || len(negatives) != 0 {
			t.Fatal("positive", e)
		}
	} else if !errors.Is(e, contract.ErrUnauthenticated) || len(negatives) != 1 || negatives[0] != "token_inactive" {
		t.Fatal("negative", e, negatives)
	}
}

func TestStatusCapacity(t *testing.T) {
	c := statusCases(t)[0]
	h := statusSetup(t, &c, 60000)
	for i := range 1025 {
		if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), fmt.Sprintf("request:%d", i)); e != nil {
			t.Fatal(e)
		}
	}
	if h.calls.Load() != 1025 {
		t.Fatal(h.calls.Load())
	}
	for i := range 1025 {
		if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), fmt.Sprintf("request:%d", i)); e != nil {
			t.Fatal(e)
		}
	}
	if h.calls.Load() <= 1025 {
		t.Fatal("unbounded reuse")
	}
}
func TestStatusUncertainInvalidation(t *testing.T) {
	cases := statusCases(t)
	c := cases[1]
	h := statusSetup(t, &c, 0)
	started := make(chan struct{})
	held := make(chan struct{})
	h.v.gate.Invalidate = func(context.Context, Negative) error { close(started); <-held; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := h.v.Verify(ctx, h.fixture.token("valid"), "session:1"); done <- e }()
	<-started
	cancel()
	if e := <-done; !errors.Is(e, contract.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	close(held)
}

func TestStatusIndependentTokens(t *testing.T) {
	c := statusCases(t)[0]
	h := statusSetup(t, &c, 60000)
	var inactive atomic.Bool
	var calls atomic.Int32
	raw := h.fixture.token("valid")
	h.v.tokens.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/keys" {
			return response(string(h.fixture.JWKS), 200), nil
		}
		if r.URL.Path == "/oauth/v2/introspect" {
			calls.Add(1)
			if e := r.ParseForm(); e != nil {
				return nil, e
			}
			if inactive.Load() && r.Form.Get("token") == raw {
				return response(`{"active":false}`, 200), nil
			}
			return response(string(c.Introspection), 200), nil
		}
		return response(string(c.User), 200), nil
	})
	ctx := context.Background()
	if _, e := h.v.Verify(ctx, raw, "a"); e != nil {
		t.Fatal(e)
	}
	other := h.fixture.token("rotated")
	if _, e := h.v.Verify(ctx, other, "b"); e != nil {
		t.Fatal(e)
	}
	inactive.Store(true)
	if _, e := h.v.Verify(ctx, raw, "c"); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if _, e := h.v.Verify(ctx, other, "b"); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 3 || len(h.negatives) != 1 || h.negatives[0].Reason != "token_inactive" {
		t.Fatal(calls.Load(), h.negatives)
	}
}

func TestStatusMalformedUnicode(t *testing.T) {
	c := statusCases(t)[0]
	h := statusSetup(t, &c, 60000)
	c.User = json.RawMessage(strings.Replace(string(c.User), `"human": {}`, `"human": {},"username":"\ud800"`, 1))
	if _, e := h.v.Verify(context.Background(), h.fixture.token("valid"), "bearer"); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
}
