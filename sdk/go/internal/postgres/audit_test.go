package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"sync"
	"testing"
)

func TestAuditInvalid(t *testing.T) {
	if _, err := NewAudit(nil, "ledger", "production", nil, nil); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
}
func TestAudit(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("live audit")
	}
	fixtures, validate := auditFixtureValidator(t)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := sql.Open("postgres", url+"&options=-c%20role%3D"+os.Getenv("HEIMDALL_TEST_AUDIT_ROLE"))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	store, err := NewAudit(runtime, "ledger", "production", []string{"primary", "secondary"}, validate)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	event := fixtures[0].Event
	if os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		receipt, err := store.Append(ctx, event)
		if err != nil || receipt.PayloadDigest != fixtures[0].Digest {
			t.Fatal("TS receipt", err)
		}
		return
	}
	rawUnicode, err := os.ReadFile("../../../../contracts/fixtures/audit-storage-unicode.json")
	if err != nil {
		t.Fatal(err)
	}
	var unicode map[string]any
	if json.Unmarshal(rawUnicode, &unicode) != nil {
		t.Fatal("unicode fixture")
	}
	if receipt, err := store.Append(ctx, unicode); err != nil || receipt.EventID != "unicode-nul-shared" {
		t.Fatal("unicode storage parity", err)
	}
	receipt, err := store.Append(ctx, event)
	if err != nil || receipt.PayloadDigest != fixtures[0].Digest {
		t.Fatal("shared receipt", err)
	}
	event["eventId"] = "go-audit-shared"
	if _, err = store.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	event["eventId"] = "go-audit-race"
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.Append(ctx, event); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func(table, id string) int {
		t.Helper()
		var n int
		if err := db.QueryRow("SELECT count(*) FROM heimdall."+table+" WHERE event_id=$1", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("audit_deliveries", "go-audit-race") != 2 {
		t.Fatal("duplicate deliveries")
	}
	event["action"] = "different"
	if _, err = store.Append(ctx, event); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("conflict", err)
	}
	event["action"] = "invoice.approve"
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = store.Append(cancelled, event); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal("cancelled", err)
	}
	if _, err = db.Exec("ALTER TABLE heimdall.audit_deliveries ADD CONSTRAINT go_test_failure CHECK(sink_id<>'z-go-blocked')"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("ALTER TABLE heimdall.audit_deliveries DROP CONSTRAINT go_test_failure")
	failing, _ := NewAudit(runtime, "ledger", "production", []string{"a-good", "z-go-blocked"}, validate)
	event["eventId"] = "go-audit-rollback"
	if _, err = failing.Append(ctx, event); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal("rollback", err)
	}
	if count("audit_events", "go-audit-rollback") != 0 || count("audit_deliveries", "go-audit-rollback") != 0 {
		t.Fatal("partial append")
	}
	event["eventId"] = "go-audit-race"
	if _, err = db.Exec("UPDATE heimdall.audit_events SET event=jsonb_set(event,'{action}','\"tampered\"') WHERE event_id='go-audit-race'"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(ctx, event); !errors.Is(err, contract.ErrAuditUnavailable) {
		t.Fatal("corruption", err)
	}
}

type auditFixture struct {
	Event  map[string]any
	Digest string
}

func auditFixtureValidator(t *testing.T) ([]auditFixture, func(map[string]any) error) {
	t.Helper()
	read := func(path string, target any) {
		t.Helper()
		raw, err := os.ReadFile("../../../../contracts/" + path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	var fixtures []auditFixture
	read("fixtures/audit-digests-v1.json", &fixtures)
	var schema any
	read("audit-event.schema.json", &schema)
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("https://test/audit", schema); err != nil {
		t.Fatal(err)
	}
	shape, err := compiler.Compile("https://test/audit")
	if err != nil {
		t.Fatal(err)
	}
	validate := func(event map[string]any) error {
		if shape.Validate(event) != nil {
			return contract.ErrInvalidArgument
		}
		raw, _ := json.Marshal(event)
		var e contract.AuditEvent
		if json.Unmarshal(raw, &e) != nil {
			return contract.ErrInvalidArgument
		}
		return contract.ValidateAuditEvent(e, "sdk", func(string, int, map[string]any) bool { return false })
	}

	return fixtures, validate
}
func TestAuditCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_AUDIT_COMMIT_LOSS")
	if url == "" {
		t.Skip("commit response loss proxy")
	}
	fixtures, validate := auditFixtureValidator(t)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	direct, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	event := fixtures[0].Event
	event["eventId"] = "go-audit-lost-commit"
	ctx := context.Background()
	store, err := NewAudit(db, "ledger", "production", []string{"primary", "secondary"}, validate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(ctx, event); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal("lost response", err)
	}
	var count int
	if err = direct.QueryRow("SELECT count(*) FROM heimdall.audit_deliveries WHERE event_id=$1", event["eventId"]).Scan(&count); err != nil || count != 2 {
		t.Fatal("committed deliveries", err)
	}
	recovered, _ := NewAudit(direct, "ledger", "production", []string{"new-sink"}, validate)
	if _, err = recovered.Append(ctx, event); err != nil {
		t.Fatal("recovered receipt", err)
	}
	if err = direct.QueryRow("SELECT count(*) FROM heimdall.audit_deliveries WHERE event_id=$1", event["eventId"]).Scan(&count); err != nil || count != 2 {
		t.Fatal("changed routing", err)
	}
}
