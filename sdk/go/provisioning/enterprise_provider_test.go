package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestEnterpriseProviderPreparation(t *testing.T) {
	var posts atomic.Int32
	var claimed atomic.Bool
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Zitadel-Orgid") != "org-a" {
			t.Error("missing trusted organization")
		}
		if r.Method == "POST" {
			posts.Add(1)
			if !claimed.Load() {
				t.Error("write preceded dispatch claim")
			}
			if fail.Load() {
				w.WriteHeader(503)
				return
			}
			_, _ = w.Write([]byte(`{"id":"provider-a"}`))
			return
		}
		_, _ = w.Write([]byte(`{"idp":{"id":"provider-a","name":"Directory","state":"IDP_STATE_ACTIVE","type":"IDP_TYPE_OIDC","details":{"resourceOwner":"org-a"},"config":{"options":{},"oidc":{"issuer":"https://directory.example","clientId":"client","scopes":["openid","email","profile"]}}}}`))
	}))
	defer server.Close()
	o := EnterpriseProviderOptions{Issuer: server.URL, OrganizationID: "org-a", Name: "Directory", Token: "token",
		TransportProfile: "loopback", OIDC: &OIDCProviderSettings{"https://directory.example", "client", "secret"},
		ClaimDispatch: func(context.Context) error { return contract.ErrConflict }}
	if _, err := PrepareEnterpriseProvider(context.Background(), o); !errors.Is(err, contract.ErrConflict) || posts.Load() != 0 {
		t.Fatal("write attempted after rejected claim", err)
	}
	o.ClaimDispatch = func(context.Context) error { claimed.Store(true); return nil }
	result, err := PrepareEnterpriseProvider(context.Background(), o)
	if err != nil || result.CallbackURL != server.URL+"/idps/callback" {
		t.Fatal("creation failed", err)
	}
	fail.Store(true)
	if _, err = PrepareEnterpriseProvider(context.Background(), o); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal("lost creation outcome was not quarantined", err)
	}
	o.ExistingProviderID, o.ClaimDispatch = "provider-a", nil
	if _, err = PrepareEnterpriseProvider(context.Background(), o); err != nil || posts.Load() != 2 {
		t.Fatal("recovery issued a write or failed", err)
	}
	o.Name = "Different"
	if _, err = PrepareEnterpriseProvider(context.Background(), o); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("mismatched provider accepted", err)
	}
}

func TestLiveEnterpriseProviderPreparation(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_ENTERPRISE_PROVIDER")
	if raw == "" {
		t.Skip("pinned ZITADEL integration")
	}
	var o EnterpriseProviderOptions
	if json.Unmarshal([]byte(raw), &o) != nil {
		t.Fatal("invalid test input")
	}
	o.ClaimDispatch = func(context.Context) error { return nil }
	result, err := PrepareEnterpriseProvider(context.Background(), o)
	if err != nil {
		var typed *contract.Error
		if errors.As(err, &typed) {
			t.Fatalf("prepare candidate: %s; id=%s", typed.Code, typed.OperationID)
		}
		t.Fatal("prepare candidate", err)
	}
	o.ExistingProviderID, o.ClaimDispatch = result.ProviderID, nil
	recovered, err := PrepareEnterpriseProvider(context.Background(), o)
	if err != nil || recovered != result {
		t.Fatal("provider recovery failed", err)
	}
	o.OrganizationID = os.Getenv("HEIMDALL_TEST_OTHER_ORGANIZATION")
	if _, err = PrepareEnterpriseProvider(context.Background(), o); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("wrong organization accepted", err)
	}
	fmt.Println("ENTERPRISE_PROVIDER_ID", result.ProviderID)
}
