package identity

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	Issuer, Audience, Subject string
	Now                       int64
	JWKS                      json.RawMessage
	Vectors                   []struct {
		Name, Token string
		Valid       bool
	}
}

func load(t *testing.T) fixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/token-verification.json")
	if e != nil {
		t.Fatal(e)
	}
	var f fixture
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f fixture) token(name string) string {
	for _, v := range f.Vectors {
		if v.Name == name {
			return v.Token
		}
	}
	panic(name)
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string, status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
func makeVerifier(t *testing.T, f fixture, now func() time.Time, fetch transport) *TokenVerifier {
	t.Helper()
	v, e := NewTokenVerifier(Config{f.Issuer, f.Audience, f.Issuer + "/keys", "production"}, now)
	if e != nil {
		t.Fatal(e)
	}
	v.client.Transport = fetch
	return v
}
func TestVectors(t *testing.T) {
	f := load(t)
	for _, vector := range f.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			v := makeVerifier(t, f, func() time.Time { return time.UnixMilli(f.Now) }, func(*http.Request) (*http.Response, error) { return response(string(f.JWKS), 200), nil })
			result, e := v.Verify(context.Background(), vector.Token)
			if vector.Valid {
				if e != nil || !v.Owns(result) || result.Issuer() != f.Issuer || result.Subject() != f.Subject || result.ExpiresAt() != f.Now+3600000 {
					t.Fatal(result, e)
				}
				other := makeVerifier(t, f, nil, nil)
				if other.Owns(result) || v.Owns(&VerifiedToken{}) {
					t.Fatal("ownership")
				}
			} else if !errors.Is(e, contract.ErrUnauthenticated) {
				t.Fatal(e)
			}
		})
	}
}
func TestRotation(t *testing.T) {
	f := load(t)
	var now atomic.Int64
	now.Store(f.Now)
	var calls atomic.Int32
	var fail atomic.Bool
	var rotated atomic.Bool
	var jwks struct{ Keys []json.RawMessage }
	_ = json.Unmarshal(f.JWKS, &jwks)
	first, _ := json.Marshal(map[string]any{"keys": jwks.Keys[:1]})
	v := makeVerifier(t, f, func() time.Time { return time.UnixMilli(now.Load()) }, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("secret")
		}
		if rotated.Load() {
			return response(string(f.JWKS), 200), nil
		}
		return response(string(first), 200), nil
	})
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := v.Verify(ctx, f.token("valid")); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	if _, e := v.Verify(ctx, f.token("rotated")); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	rotated.Store(true)
	now.Add(30000)
	if _, e := v.Verify(ctx, f.token("rotated")); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	fail.Store(true)
	now.Add(30000)
	if _, e := v.Verify(ctx, f.token("unknown-key")); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := v.Verify(ctx, f.token("valid")); e != nil {
		t.Fatal(e)
	}
	if _, e := v.Verify(ctx, f.token("unknown-key")); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
	now.Add(300000)
	if _, e := v.Verify(ctx, f.token("valid")); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if calls.Load() != 4 {
		t.Fatal(calls.Load())
	}
	fail.Store(false)
	now.Add(30000)
	if _, e := v.Verify(ctx, f.token("valid")); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 5 {
		t.Fatal(calls.Load())
	}
}
func TestCancellation(t *testing.T) {
	f := load(t)
	release := make(chan struct{})
	started := make(chan struct{})
	v := makeVerifier(t, f, func() time.Time { return time.UnixMilli(f.Now) }, func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return response(string(f.JWKS), 200), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, e := v.Verify(ctx, f.token("valid")); result <- e }()
	<-started
	cancel()
	if e := <-result; !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	close(release)
	if _, e := v.Verify(context.Background(), f.token("valid")); e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-deadline.Done()
	if _, e := v.Verify(deadline, f.token("valid")); !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
}
func TestMalformedKeyset(t *testing.T) {
	f := load(t)
	var jwks struct{ Keys []map[string]any }
	_ = json.Unmarshal(f.JWKS, &jwks)
	variants := []any{map[string]any{"keys": []any{}}, map[string]any{"keys": []any{jwks.Keys[0], jwks.Keys[0]}}, map[string]any{"keys": make([]any, 17)}}
	for _, patch := range []map[string]any{{"d": "secret"}, {"alg": "HS256"}, {"n": "AQ"}, {"e": "Ag"}, {"key_ops": []string{"sign"}}} {
		copy := map[string]any{}
		for k, x := range jwks.Keys[0] {
			copy[k] = x
		}
		for k, x := range patch {
			copy[k] = x
		}
		variants = append(variants, map[string]any{"keys": []any{copy}})
	}
	for _, bad := range variants {
		raw, _ := json.Marshal(bad)
		v := makeVerifier(t, f, func() time.Time { return time.UnixMilli(f.Now) }, func(*http.Request) (*http.Response, error) { return response(string(raw), 200), nil })
		if _, e := v.Verify(context.Background(), f.token("valid")); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal(e)
		}
	}
	for _, bad := range []struct {
		status int
		body   string
	}{{200, strings.Repeat("x", 262145)}, {302, "redirect"}, {503, "down"}} {
		v := makeVerifier(t, f, func() time.Time { return time.UnixMilli(f.Now) }, func(*http.Request) (*http.Response, error) { return response(bad.body, bad.status), nil })
		if _, e := v.Verify(context.Background(), f.token("valid")); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal(e)
		}
	}
	if _, e := NewTokenVerifier(Config{f.Issuer, f.Audience, "https://attacker.example/keys", "production"}, nil); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
}
func TestSubjectEncoding(t *testing.T) {
	for raw, want := range map[string]bool{`"Maya\u0000東京"`: true, `"\ud83d\ude00"`: true, `"\ud800"`: false, `"\udfff"`: false, `"\\ud800"`: true} {
		if validSubjectJSON([]byte(raw)) != want {
			t.Fatal(raw)
		}
	}
}

func TestLiveToken(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_TOKEN_CASES")
	if raw == "" {
		t.Skip("live provider tokens")
	}
	var c struct {
		Issuer, Audience, JWKSURI, OtherAudience, Token, Subject string
		Forged                                                   []string
	}
	if json.Unmarshal([]byte(raw), &c) != nil {
		t.Fatal("invalid live config")
	}
	metadata, e := DiscoverOIDC(context.Background(), c.Issuer, "production")
	if e != nil {
		t.Fatal(e)
	}
	if metadata.JWKSURI != c.JWKSURI {
		t.Fatal("discovery binding")
	}
	v, e := NewTokenVerifier(Config{c.Issuer, c.Audience, metadata.JWKSURI, "production"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	result, e := v.Verify(context.Background(), c.Token)
	if e != nil || result.Subject() != c.Subject || !v.Owns(result) {
		t.Fatal("valid token", e)
	}
	other, e := NewTokenVerifier(Config{c.Issuer, c.OtherAudience, metadata.JWKSURI, "production"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.Verify(context.Background(), c.Token); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal("audience", e)
	}
	for _, forged := range c.Forged {
		if _, e = v.Verify(context.Background(), forged); !errors.Is(e, contract.ErrUnauthenticated) {
			t.Fatal("forged token", e)
		}
	}
}

func TestBackwardClock(t *testing.T) {
	f := load(t)
	var now atomic.Int64
	now.Store(f.Now)
	v := makeVerifier(t, f, func() time.Time { return time.UnixMilli(now.Load()) }, func(*http.Request) (*http.Response, error) { return response(string(f.JWKS), 200), nil })
	if _, e := v.Verify(context.Background(), f.token("valid")); e != nil {
		t.Fatal(e)
	}
	now.Add(-1)
	if _, e := v.Verify(context.Background(), f.token("valid")); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
}

func TestRedirectRefusal(t *testing.T) {
	f := load(t)
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/keys" {
			http.Redirect(w, r, "/untrusted", 302)
		} else {
			redirected.Add(1)
			_, _ = w.Write(f.JWKS)
		}
	}))
	defer server.Close()
	v, e := NewTokenVerifier(Config{server.URL, f.Audience, server.URL + "/keys", "loopback"}, func() time.Time { return time.UnixMilli(f.Now) })
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.Verify(context.Background(), f.token("valid")); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestNetworkDeadline(t *testing.T) {
	f := load(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	v, e := NewTokenVerifier(Config{server.URL, f.Audience, server.URL + "/keys", "loopback"}, func() time.Time { return time.UnixMilli(f.Now) })
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.Verify(context.Background(), f.token("valid")); !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
}
