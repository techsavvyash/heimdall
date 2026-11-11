package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

func TestAuditAcknowledgements(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live acknowledgements")
	}
	fixtures, validate := auditFixtureValidator(t)
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deliveryDB, err := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_audit_delivery")
	if err != nil {
		t.Fatal(err)
	}
	defer deliveryDB.Close()
	writer, _ := NewAudit(db, "ledger", "production", []string{"siem", "other-sink"}, validate)
	store, _ := NewAudit(deliveryDB, "ledger", "production", nil, validate)
	ctx := context.Background()
	raw, err := os.ReadFile("../../../../contracts/fixtures/audit-delivery-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name           string
		Batch, Receipt contract.AuditDelivery
		Valid          bool
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixtures")
	}
	for index, item := range cases {
		prefix := fmt.Sprintf("go-ack-%d-", index)
		ids := []string{prefix + "e1", prefix + "e2"}
		for _, id := range ids {
			event := fixtures[0].Event
			event["eventId"] = id
			if _, err = writer.Append(ctx, event); err != nil {
				t.Fatal(err)
			}
		}
		for i, id := range item.Batch.EventIDs {
			item.Batch.EventIDs[i] = prefix + id
		}
		for i, id := range item.Receipt.EventIDs {
			item.Receipt.EventIDs[i] = prefix + id
		}
		err = store.Acknowledge(ctx, item.Batch, item.Receipt, 1000)
		if (err == nil) != item.Valid {
			t.Fatalf("%s: %v", item.Name, err)
		}
		for _, id := range ids {
			for _, sink := range []string{"siem", "other-sink"} {
				want := "pending"
				if item.Valid && sink == "siem" {
					for _, acked := range item.Receipt.EventIDs {
						if acked == id {
							want = "delivered"
						}
					}
				}
				var state string
				if err = db.QueryRow("SELECT state FROM heimdall.audit_deliveries WHERE event_id=$1 AND sink_id=$2", id, sink).Scan(&state); err != nil || state != want {
					t.Fatalf("%s state %s want %s: %v", item.Name, state, want, err)
				}
			}
		}
	}
	ids := []string{"go-ack-atomic-1", "go-ack-atomic-2"}
	for _, id := range ids {
		fixtures[0].Event["eventId"] = id
		if _, err = writer.Append(ctx, fixtures[0].Event); err != nil {
			t.Fatal(err)
		}
	}
	batch := contract.AuditDelivery{ProductID: "ledger", Environment: "production", SinkID: "siem", EventIDs: ids}
	if _, err = db.Exec("UPDATE heimdall.audit_deliveries SET state='quarantined' WHERE event_id=$1 AND sink_id='siem'", ids[1]); err != nil {
		t.Fatal(err)
	}
	if err = store.Acknowledge(ctx, batch, batch, 2000); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal(err)
	}
	var state string
	if err = db.QueryRow("SELECT state FROM heimdall.audit_deliveries WHERE event_id=$1 AND sink_id='siem'", ids[0]).Scan(&state); err != nil || state != "pending" {
		t.Fatal("partial update", err)
	}
	missing := batch
	missing.EventIDs = []string{ids[0], "missing-route"}
	if err = store.Acknowledge(ctx, missing, missing, 2000); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE heimdall.audit_deliveries SET state='pending' WHERE event_id=$1 AND sink_id='siem'", ids[1]); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- store.Acknowledge(ctx, batch, batch, 3000) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Acknowledge(ctx, batch, batch, 4000); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		var at int64
		if err = db.QueryRow("SELECT delivered_at FROM heimdall.audit_deliveries WHERE event_id=$1 AND sink_id='siem'", id).Scan(&at); err != nil || at != 3000 {
			t.Fatal("replay changed timestamp", err)
		}
	}
}

func TestAcknowledgementCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_ACK_COMMIT_LOSS")
	if url == "" {
		t.Skip("ack response loss proxy")
	}
	fixtures, validate := auditFixtureValidator(t)
	fault, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Close()
	direct, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	writer, _ := NewAudit(direct, "ledger", "production", []string{"siem"}, validate)
	store, _ := NewAudit(fault, "ledger", "production", nil, validate)
	ctx := context.Background()
	ids := []string{"go-ack-loss-1", "go-ack-loss-2"}
	for _, id := range ids {
		fixtures[0].Event["eventId"] = id
		if _, err = writer.Append(ctx, fixtures[0].Event); err != nil {
			t.Fatal(err)
		}
	}
	batch := contract.AuditDelivery{ProductID: "ledger", Environment: "production", SinkID: "siem", EventIDs: ids}
	receipt := batch
	receipt.EventIDs = []string{ids[0]}
	if err = store.Acknowledge(ctx, batch, receipt, 1234); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal("lost ack response", err)
	}
	verify := func() {
		t.Helper()
		for i, id := range ids {
			var state string
			var at sql.NullInt64
			if err = direct.QueryRow("SELECT state,delivered_at FROM heimdall.audit_deliveries WHERE event_id=$1", id).Scan(&state, &at); err != nil {
				t.Fatal(err)
			}
			if i == 0 && (state != "delivered" || !at.Valid || at.Int64 != 1234) || i == 1 && (state != "pending" || at.Valid) {
				t.Fatal("unexpected acknowledgement state", state, at)
			}
		}
	}
	verify()
	if err = writer.Acknowledge(ctx, batch, receipt, 2345); err != nil {
		t.Fatal(err)
	}
	verify()
}
