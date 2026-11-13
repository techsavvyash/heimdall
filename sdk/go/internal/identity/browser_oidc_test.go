package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestBrowserOIDCExchange(t *testing.T) {
	var f struct {
		ClientID, ClientSecret, Audience, Subject, Code, Nonce, Verifier string
		Cases                                                            []struct{ Name, Error string }
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/browser-oidc.json")
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("invalid exchange fixture", err)
	}
	var discovery struct {
		Issuer   string
		Metadata map[string]any
	}
	raw, err = os.ReadFile("../../../../contracts/fixtures/oidc-discovery.json")
	if err != nil || json.Unmarshal(raw, &discovery) != nil {
		t.Fatal("invalid discovery fixture", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	jwks := map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": "test", "n": encode(key.N.Bytes()), "e": "AQAB",
	}}}
	sign := func(issuer string, claims jwt.MapClaims) string {
		claims["iss"], claims["iat"], claims["exp"] = issuer, time.Now().Unix(), time.Now().Add(5*time.Minute).Unix()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "test"
		raw, err := token.SignedString(key)
		if err != nil {
			t.Error(err)
		}
		return raw
	}
	var mode atomic.Value
	mode.Store("")
	var posts atomic.Int64
	const redirect = "https://ledger.example.test/auth/callback"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := "http://" + r.Host
		if r.URL.Path == strings.TrimPrefix(discovery.Metadata["jwks_uri"].(string), discovery.Issuer) {
			_ = json.NewEncoder(w).Encode(jwks)
			return
		}
		if r.URL.Path == "/.well-known/openid-configuration" {
			metadata := map[string]any{}
			for name, value := range discovery.Metadata {
				if endpoint, ok := value.(string); ok && strings.HasPrefix(endpoint, discovery.Issuer) {
					value = issuer + strings.TrimPrefix(endpoint, discovery.Issuer)
				}
				metadata[name] = value
			}
			_ = json.NewEncoder(w).Encode(metadata)
			return
		}
		if r.URL.Path != strings.TrimPrefix(discovery.Metadata["token_endpoint"].(string), discovery.Issuer) {
			t.Error("unexpected request", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		posts.Add(1)
		username, password, ok := r.BasicAuth()
		if !ok || username != f.ClientID || password != "fixture%3Asecret%2B*%7E" ||
			r.Method != http.MethodPost || r.ParseForm() != nil ||
			r.Form.Get("code") != f.Code || r.Form.Get("redirect_uri") != redirect ||
			r.Form.Get("code_verifier") != f.Verifier || r.Form.Get("grant_type") != "authorization_code" {
			t.Error("incorrect exchange request")
		}
		scenario := mode.Load().(string)
		switch scenario {
		case "redirect":
			http.Redirect(w, r, "/must-not-follow", http.StatusFound)
			return
		case "oversize":
			_, _ = w.Write([]byte(strings.Repeat(" ", 65537)))
			return
		case "invalid grant":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		access := jwt.MapClaims{"aud": f.Audience, "sub": f.Subject}
		if scenario == "wrong access subject" {
			access["sub"] = "other"
		}
		if scenario == "wrong access audience" {
			access["aud"] = f.ClientID
		}
		accessToken := sign(issuer, access)
		digest := sha256.Sum256([]byte(accessToken))
		id := jwt.MapClaims{"aud": []string{f.ClientID}, "sub": f.Subject, "nonce": f.Nonce, "azp": f.ClientID, "at_hash": encode(digest[:16])}
		switch scenario {
		case "project and client audiences":
			id["aud"] = []string{f.ClientID, f.Audience}
		case "missing authorized party with two audiences":
			id["aud"] = []string{f.ClientID, f.Audience}
			delete(id, "azp")
		case "wrong nonce":
			id["nonce"] = "other"
		case "wrong ID audience":
			id["aud"] = f.Audience
		case "extra ID audience":
			id["aud"] = []string{f.ClientID, "other"}
		case "wrong authorized party":
			id["azp"] = "other"
		case "wrong access hash":
			id["at_hash"] = "other"
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{accessToken, sign(issuer, id), "refresh-fixture", "Bearer"})
	}))
	defer server.Close()
	ctx := context.Background()
	browser, err := NewBrowserOIDC(ctx, BrowserOIDCConfig{Issuer: server.URL, Profile: "loopback", ClientID: f.ClientID, Audience: f.Audience, ClientSecret: f.ClientSecret, RedirectURI: redirect})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range f.Cases {
		t.Run(scenario.Name, func(t *testing.T) {
			mode.Store(scenario.Name)
			before := posts.Load()
			result, err := browser.Exchange(ctx, f.Code, func() (string, string, error) {
				if scenario.Name == "claim uncertain" {
					return "", "", contract.ErrOutcomeUnknown
				}
				return f.Nonce, f.Verifier, nil
			})
			if scenario.Error == "" {
				if err != nil || result == nil || result.Identity.Subject() != f.Subject || browser.verifier.Owns(result.Identity) || result.RefreshToken != "refresh-fixture" {
					t.Fatal("invalid exchange result", err)
				}
			} else if result != nil || !errors.Is(err, contract.NewError(contract.Code(scenario.Error), "")) {
				t.Fatal("unexpected exchange result", err)
			}
			want := int64(1)
			if scenario.Name == "claim uncertain" {
				want = 0
			}
			if posts.Load()-before != want {
				t.Fatal("incorrect exchange count")
			}
		})
	}
}

func TestLiveBrowserExchange(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_BROWSER_EXCHANGE")
	if raw == "" {
		t.Skip("pinned provider integration")
	}
	var f struct {
		Config                             BrowserOIDCConfig
		Code, Nonce, PKCEVerifier, Subject string
	}
	if json.Unmarshal([]byte(raw), &f) != nil {
		t.Fatal("invalid live exchange input")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	browser, err := NewBrowserOIDC(ctx, f.Config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := browser.Exchange(ctx, f.Code, func() (string, string, error) {
		return f.Nonce, f.PKCEVerifier, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity.Subject() != f.Subject || result.RefreshToken == "" || result.AccessExpiresAt <= time.Now().UnixMilli() {
		t.Fatal("unexpected verified exchange result")
	}
	cfg := browser.verifier.config
	status, err := NewIdentityVerifier(StatusConfig{
		Config: cfg, ProductID: "probe", Environment: "test", PolicyVersion: "p1", ClientID: f.Config.ClientID,
	}, func(context.Context) (string, error) { return f.Config.ClientSecret, nil }, Gate{
		Check:      func(context.Context, Identity) (bool, error) { return false, nil },
		Invalidate: func(context.Context, Negative) error { return nil },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := status.Verify(ctx, result.AccessToken, "bearer")
	if err != nil || principal.Identity().Subject != f.Subject {
		t.Fatal("live identity status failed", err)
	}
}
