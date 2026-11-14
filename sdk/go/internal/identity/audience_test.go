package identity

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAudienceBoundaries(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/audience-boundaries.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Issuer string
		Now    int64
		JWKS   json.RawMessage
		Cases  []struct {
			Name, Character, Token string
			Count                  int
			Valid                  bool
		}
	}
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	raw, e = os.ReadFile("../../../../contracts/fixtures/configuration-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var base struct {
		Config   map[string]any
		Manifest json.RawMessage
	}
	if json.Unmarshal(raw, &base) != nil {
		t.Fatal("base")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			audience := strings.Repeat(c.Character, c.Count)
			cfg := Config{Issuer: f.Issuer, Audience: audience, JWKSURI: f.Issuer + "/keys", Profile: "production"}
			base.Config["audience"] = audience
			base.Config["issuer"] = f.Issuer
			raw, _ := json.Marshal(base.Config)
			deployment, parseError := configuration.Parse(raw, base.Manifest)
			verifier, e := NewTokenVerifier(cfg, func() time.Time { return time.UnixMilli(f.Now) })
			if !c.Valid {
				if !errors.Is(e, contract.ErrInvalidArgument) || !errors.Is(parseError, contract.ErrInvalidArgument) {
					t.Fatal(e, parseError)
				}
				return
			}
			if e != nil || parseError != nil {
				t.Fatal(e, parseError)
			}
			if deployment.Config().Audience != audience {
				t.Fatal("configuration changed audience")
			}
			verifier.client.Transport = transport(func(*http.Request) (*http.Response, error) { return response(string(f.JWKS), 200), nil })
			principal, e := verifier.Verify(context.Background(), c.Token)
			if e != nil || !verifier.Owns(principal) || principal.Subject() != "audience-test" {
				t.Fatal(e)
			}
			cfg.Audience = "another-product"
			other, e := NewTokenVerifier(cfg, func() time.Time { return time.UnixMilli(f.Now) })
			if e != nil {
				t.Fatal(e)
			}
			other.client.Transport = verifier.client.Transport
			if _, e = other.Verify(context.Background(), c.Token); !errors.Is(e, contract.ErrUnauthenticated) {
				t.Fatal(e)
			}
		})
	}
	if _, e := NewTokenVerifier(Config{Issuer: f.Issuer, Audience: string([]byte{0xff}), JWKSURI: f.Issuer + "/keys", Profile: "production"}, nil); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal("malformed UTF8", e)
	}
}
