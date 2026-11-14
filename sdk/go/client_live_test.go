package heimdall_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	postgres "github.com/techsavvyash/heimdall/sdk/go/postgres"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestLiveRuntime(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_RUNTIME")
	if raw == "" || os.Getenv("HEIMDALL_TEST_RUNTIME_DATABASE_URL") != "" {
		t.Skip("optional live runtime")
	}
	var c struct {
		Config, Manifest                                        json.RawMessage
		ClientSecret, Token, Subject, Permission, EffectiveRole string
		ExpectedRoles                                           []string
		DenyAcme                                                bool
		RejectModel                                             bool
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
	if c.RejectModel {
		if !errors.Is(e, heimdall.ErrUnavailable) {
			t.Fatal("mismatched model did not fail startup", e)
		}
		return
	}
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
	if c.Permission != "" {
		for _, id := range []string{"acme", "globex"} {
			allowed, err := client.Tenant(id).Can(context.Background(), p, c.Permission)
			if err != nil || allowed != (id == "acme" && !c.DenyAcme) {
				t.Fatal("unexpected permission result", err)
			}
		}
	}
	if c.Permission != "" {
		for _, id := range []string{"acme", "globex"} {
			roles, err := client.Tenant(id).Roles(context.Background(), p)
			want := ""
			if id == "acme" {
				want = c.EffectiveRole
				if c.ExpectedRoles != nil {
					want = strings.Join(c.ExpectedRoles, ",")
				}
			}
			if err != nil || strings.Join(roles, ",") != want {
				t.Fatal("unexpected effective roles", err)
			}
		}
	}
	if c.Permission != "" {
		discovery, err := client.NewWorkspaceDiscovery(heimdall.DiscoveryOptions{
			ActiveKeyID: "fixture", Keys: map[string][]byte{"fixture": make([]byte, 32)},
			Source: workspaceScan(func(context.Context, heimdall.ScanOptions) (heimdall.CandidatePage, error) {
				return heimdall.CandidatePage{Items: []heimdall.Candidate{
					{Position: "a", Workspace: heimdall.Workspace{ID: "acme", Name: "Acme"}},
					{Position: "b", Workspace: heimdall.Workspace{ID: "globex", Name: "Globex"}},
				}, Exhausted: true}, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		page, err := discovery.List(context.Background(), p, heimdall.WorkspaceListOptions{})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != "acme" || page.NextCursor != "" {
			t.Fatal("unexpected discovery page", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	expected := "succeeded,deny"
	if c.Permission != "" && !c.DenyAcme {
		expected += ",requested,allow,requested,deny"
	}
	if !strings.HasPrefix(strings.Join(outcomes, ","), expected) {
		t.Fatal("audit outcomes")
	}
}

func TestLivePostgresRuntime(t *testing.T) {
	raw, url := os.Getenv("HEIMDALL_TEST_RUNTIME"), os.Getenv("HEIMDALL_TEST_RUNTIME_DATABASE_URL")
	if raw == "" || url == "" {
		t.Skip("optional live durable runtime")
	}
	var c struct {
		Config                       map[string]any
		Manifest                     json.RawMessage
		ClientSecret, Token, Subject string
	}
	if json.Unmarshal([]byte(raw), &c) != nil {
		t.Fatal("runtime input")
	}
	product, environment := c.Config["productId"].(string), c.Config["environment"].(string)+"-go"
	c.Config["environment"] = environment
	config, e := json.Marshal(c.Config)
	if e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_public_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	storage, e := postgres.NewStorage(db, postgres.StorageOptions{ProductID: product, Environment: environment, AuditSinks: []string{"siem"}})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	client, e := heimdall.New(ctx, config, c.Manifest, heimdall.Options{PolicyVersion: "runtime-v1", Credentials: heimdall.Credentials{ClientSecret: func(context.Context) (string, error) { return c.ClientSecret, nil }, OpenFGAKey: func(context.Context) (string, error) { return os.Getenv("HEIMDALL_PROBE_OPENFGA_KEY"), nil }}, Storage: storage})
	if e != nil {
		t.Fatal(e)
	}
	principal, e := client.Tokens().Verify(ctx, c.Token)
	if e != nil {
		t.Fatal(e)
	}
	if principal.Identity().Subject != c.Subject {
		t.Fatal("subject mismatch")
	}
	if _, e = client.Tokens().Verify(ctx, "invalid"); !errors.Is(e, heimdall.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if e = storage.Identity.Invalidate(ctx, heimdall.IdentityNegative{Identity: principal.Identity(), Context: "bearer", TokenDigest: strings.Repeat("a", 64), Reason: "identity_inactive"}); e != nil {
		t.Fatal(e)
	}
	if _, e = client.Tokens().Verify(ctx, c.Token); !errors.Is(e, heimdall.ErrUnauthenticated) {
		t.Fatal(e)
	}
	rows, e := db.QueryContext(ctx, "SELECT event FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2", product, environment)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	outcomes := map[string]int{}
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			t.Fatal("row")
		}
		var wrapper struct{ Payload string }
		if json.Unmarshal(raw, &wrapper) != nil {
			t.Fatal("wrapper")
		}
		decoded, e := base64.RawURLEncoding.DecodeString(wrapper.Payload)
		if e != nil {
			t.Fatal("payload")
		}
		var event struct{ ProductID, Environment, Outcome string }
		if json.Unmarshal(decoded, &event) != nil {
			t.Fatal("event")
		}
		if event.ProductID != product || event.Environment != environment {
			t.Fatal("scope")
		}
		outcomes[event.Outcome]++
	}
	if rows.Err() != nil {
		t.Fatal("read audit")
	}
	if len(outcomes) != 2 || outcomes["succeeded"] != 1 || outcomes["deny"] != 2 {
		t.Fatal("audit outcomes")
	}
	var count int
	if e = db.QueryRowContext(ctx, "SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id=$1 AND environment=$2 AND sink_id='siem'", product, environment).Scan(&count); e != nil || count != 3 {
		t.Fatal("delivery count")
	}
}
