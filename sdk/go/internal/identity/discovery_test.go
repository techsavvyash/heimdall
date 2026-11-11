package identity

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoveryVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/fixtures/oidc-discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Issuer   string
		Metadata map[string]any
		Cases    []struct {
			Name  string
			Patch map[string]any
			Valid bool
		}
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			data := map[string]any{}
			for k, v := range f.Metadata {
				data[k] = v
			}
			for k, v := range c.Patch {
				data[k] = v
			}
			raw, _ := json.Marshal(data)
			result, err := ValidateOIDCMetadata(raw, f.Issuer, "production")
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v err=%v", c.Valid, err)
			}
			if c.Valid && result.Issuer != f.Issuer {
				t.Fatal("issuer")
			}
		})
	}
}
func TestDiscoveryTransport(t *testing.T) {
	var requests atomic.Int64
	var mode atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch mode.Load() {
		case 0:
			w.Header().Set("Location", "/other")
			w.WriteHeader(302)
		case 1:
			<-r.Context().Done()
		case 2:
			_, _ = w.Write([]byte(strings.Repeat(" ", 65537)))
		case 3:
			_, _ = w.Write([]byte("{"))
		}
	}))
	defer server.Close()
	if _, err := DiscoverOIDC(context.Background(), server.URL, "loopback"); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("followed redirect")
	}
	mode.Store(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := DiscoverOIDC(ctx, server.URL, "loopback"); !errors.Is(err, contract.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
	for _, m := range []int64{2, 3} {
		mode.Store(m)
		if _, err := DiscoverOIDC(context.Background(), server.URL, "loopback"); !errors.Is(err, contract.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	before := requests.Load()
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := DiscoverOIDC(ctx, server.URL, "loopback"); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal(err)
	}
	if _, err := DiscoverOIDC(context.Background(), server.URL, "production"); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if requests.Load() != before {
		t.Fatal("unexpected request")
	}
}
func TestLiveDiscovery(t *testing.T) {
	issuer := os.Getenv("HEIMDALL_TEST_DISCOVERY_ISSUER")
	if issuer == "" {
		t.Skip("optional live provider")
	}
	metadata, err := DiscoverOIDC(context.Background(), issuer, "production")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Issuer != issuer || metadata.JWKSURI != issuer+"/oauth/v2/keys" {
		t.Fatal("unexpected binding")
	}
}
