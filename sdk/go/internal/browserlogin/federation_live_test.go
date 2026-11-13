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

	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
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
	metadata, err := identity.DiscoverOIDC(ctx, input.Config.Issuer, input.Config.Profile)
	if err != nil {
		t.Fatal(err)
	}
	policy := input.Policy
	source := func(context.Context, string) (contract.LoginPolicy, int64, error) {
		return policy, 300000, nil
	}
	var audits int
	auth, err := auditflow.NewAuthentication(identity.StatusConfig{
		Config:    identity.Config{Issuer: input.Config.Issuer, Audience: input.Config.Audience, JWKSURI: metadata.JWKSURI, Profile: input.Config.Profile},
		ProductID: "federation", Environment: "verification", PolicyVersion: "probe-runtime", ClientID: input.Config.ClientID,
	}, func(context.Context) (string, error) { return input.Config.ClientSecret, nil }, identity.Gate{
		Check:      func(context.Context, identity.Identity) (bool, error) { return false, nil },
		Invalidate: func(context.Context, identity.Negative) error { return nil },
	}, func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
		audits++
		digest, err := contract.AuditDigest(event)
		return contract.AuditReceipt{ProductID: "federation", Environment: "verification", EventID: event["eventId"].(string),
			RecordedAt: int64(event["recordedAt"].(float64)), PayloadDigest: digest}, err
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessions(adapter, auth, store, "/", 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureFederation(FederationOptions{Policy: source, Credential: func(context.Context) (string, error) { return input.Credential, nil }})
	w := httptest.NewRecorder()
	if err := sessions.StartFederation(w, httptest.NewRequest(http.MethodGet, input.Config.RedirectURI, nil).WithContext(ctx), "acme"); err != nil {
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
	complete := func(callback string, cookie bool) (*http.Response, error) {
		r := httptest.NewRequest(http.MethodGet, callback, nil).WithContext(ctx)
		if cookie {
			r.AddCookie(start.Cookies()[0])
		}
		w := httptest.NewRecorder()
		err := sessions.Callback(w, r)
		return w.Result(), err
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
	policy.PolicyVersion = "rotated"
	if _, err := complete(bridge.Callback, true); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("changed policy was accepted", err)
	}
	policy = input.Policy
	result, err := complete(bridge.Callback, true)
	if err != nil || result.StatusCode != 303 || len(result.Cookies()) != 2 {
		t.Fatal("callback failed", err)
	}
	r := httptest.NewRequest(http.MethodGet, input.Config.RedirectURI, nil).WithContext(ctx)
	r.AddCookie(result.Cookies()[1])
	principal, err := sessions.Authenticate(r)
	if err != nil || principal.Identity().Subject != input.Subject || audits != 2 {
		t.Fatal("audited session did not authenticate", err)
	}
	evidence := identity.EnterpriseEvidence(principal)
	if evidence == nil || evidence.TenantID != "acme" || evidence.ProviderID != input.Policy.ProviderID || evidence.Protocol != input.Policy.Protocol {
		t.Fatal("unexpected saved evidence")
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
