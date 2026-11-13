package browserlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func TestLiveFederationHTTP(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_FEDERATION_HTTP")
	if raw == "" {
		t.Skip("pinned provider integration")
	}
	var input struct {
		Config                                                identity.BrowserOIDCConfig
		Policy                                                contract.LoginPolicy
		Credential, FixtureOrigin, Bridge, BridgeKey, Subject string
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal("invalid federation fixture")
	}
	input.Policy.TenantID, input.Policy.StrictSSO = "acme", true
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store, err := NewCustomSessionStore("federation", "verification", SessionStoreOptions{
		Store: &memorySessionStore{rows: make(map[string]StoredSessionRecord)}, ActiveKeyID: "probe",
		Keys:            map[string][]byte{"probe": bytes.Repeat([]byte{7}, 32)},
		ReserveKeyUsage: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewHTTP(ctx, input.Config, postgres.NewBrowserLoginRecords(store, nil), "federation", "verification", "default")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := adapter.StartFederation(w, httptest.NewRequest(http.MethodGet, input.Config.RedirectURI, nil).WithContext(ctx), input.Policy, input.Credential); err != nil {
		t.Fatal("start", err)
	}
	start := w.Result()
	defer start.Body.Close()
	authorization, err := url.Parse(start.Header.Get("Location"))
	if err != nil || start.StatusCode != 303 || authorization.Scheme+"://"+authorization.Host != input.FixtureOrigin || len(start.Cookies()) != 1 {
		t.Fatal("invalid start response")
	}
	body, _ := json.Marshal(map[string]string{"authorizationUrl": authorization.String()})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, input.Bridge+"/upstream", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+input.BridgeKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("bridge failed")
	}
	defer response.Body.Close()
	var bridge struct{ Callback string }
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&bridge) != nil {
		t.Fatal("bridge rejected federation")
	}
	complete := func(callback string, cookie bool) (*CallbackResult, error) {
		r := httptest.NewRequest(http.MethodGet, callback, nil).WithContext(ctx)
		if cookie {
			r.AddCookie(start.Cookies()[0])
		}
		return adapter.FederationCallback(r, input.Policy, 300000, input.Credential)
	}
	if _, err := complete(bridge.Callback, false); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("missing cookie was accepted", err)
	}
	changed, err := url.Parse(bridge.Callback)
	if err != nil {
		t.Fatal("invalid fixture callback")
	}
	query := changed.Query()
	query.Set("id", "different-intent")
	changed.RawQuery = query.Encode()
	if _, err := complete(changed.String(), true); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("wrong intent was accepted", err)
	}
	rotated := input.Policy
	rotated.PolicyVersion = "rotated"
	rotationRequest := httptest.NewRequest(http.MethodGet, bridge.Callback, nil).WithContext(ctx)
	rotationRequest.AddCookie(start.Cookies()[0])
	if _, err := adapter.FederationCallback(rotationRequest, rotated, 300000, input.Credential); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("changed policy was accepted", err)
	}
	result, err := complete(bridge.Callback, true)
	if err != nil {
		t.Fatal("callback failed", err)
	}
	evidence := result.EnterpriseEvidence
	if evidence == nil || evidence.Subject != input.Subject || evidence.TenantID != "acme" ||
		evidence.ProviderID != input.Policy.ProviderID || evidence.Protocol != input.Policy.Protocol ||
		evidence.OrganizationID != input.Policy.OrganizationID {
		t.Fatal("unexpected federation evidence")
	}
	body, _ = json.Marshal(map[string]string{"sessionId": evidence.ProviderSessionID})
	cleanup, _ := http.NewRequestWithContext(ctx, http.MethodPost, input.Bridge+"/session", bytes.NewReader(body))
	cleanup.Header.Set("Authorization", "Bearer "+input.BridgeKey)
	cleaned, err := http.DefaultClient.Do(cleanup)
	if err != nil {
		t.Fatal("could not register cleanup")
	}
	cleaned.Body.Close()
	if cleaned.StatusCode != 204 {
		t.Fatal("cleanup registration rejected")
	}
	if _, err := complete(bridge.Callback, true); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("replay was accepted", err)
	}
}
