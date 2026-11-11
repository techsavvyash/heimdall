package heimdall_test

import (
	"context"
	"encoding/json"
	"errors"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"os"
	"sync"
	"testing"
)

func TestLiveRuntime(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_RUNTIME")
	if raw == "" {
		t.Skip("optional live runtime")
	}
	var c struct {
		Config, Manifest             json.RawMessage
		ClientSecret, Token, Subject string
	}
	if json.Unmarshal([]byte(raw), &c) != nil {
		t.Fatal("runtime input")
	}
	var scope struct{ ProductID, Environment string }
	if json.Unmarshal(c.Config, &scope) != nil {
		t.Fatal("scope")
	}
	var mu sync.Mutex
	var outcomes []string
	zero := int64(0)
	client, e := heimdall.New(context.Background(), c.Config, c.Manifest, heimdall.Options{PolicyVersion: "runtime-v1", IdentityFreshnessMs: &zero, Credentials: heimdall.Credentials{ClientSecret: func(context.Context) (string, error) { return c.ClientSecret, nil }, OpenFGAKey: func(context.Context) (string, error) { return os.Getenv("HEIMDALL_PROBE_OPENFGA_KEY"), nil }}, Storage: heimdall.RuntimeStorage{ProductID: scope.ProductID, Environment: scope.Environment, Ready: func(context.Context) error { return nil }, Identity: heimdall.IdentityGate{Check: func(context.Context, heimdall.Identity) (bool, error) { return false, nil }, Invalidate: func(context.Context, heimdall.IdentityNegative) error { return nil }}, AppendAudit: func(_ context.Context, event map[string]any) (heimdall.AuditReceipt, error) {
		mu.Lock()
		defer mu.Unlock()
		outcomes = append(outcomes, event["outcome"].(string))
		digest, e := heimdall.AuditEventDigest(event)
		return heimdall.AuditReceipt{ProductID: event["productId"].(string), Environment: event["environment"].(string), EventID: event["eventId"].(string), RecordedAt: int64(event["recordedAt"].(float64)), PayloadDigest: digest}, e
	}}})
	if e != nil {
		t.Fatal(e)
	}
	p, e := client.Tokens().Verify(context.Background(), c.Token)
	if e != nil || p.Identity().Subject != c.Subject {
		t.Fatal(e)
	}
	if _, e = client.Tokens().Verify(context.Background(), "invalid"); !errors.Is(e, heimdall.ErrUnauthenticated) {
		t.Fatal(e)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(outcomes) != 2 || outcomes[0] != "succeeded" || outcomes[1] != "deny" {
		t.Fatal("audit outcomes")
	}
}
