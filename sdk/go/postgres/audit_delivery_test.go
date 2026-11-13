package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestAuditDeliveryPostgres(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_AUDIT_DELIVERY_DB")
	if dsn == "" {
		t.Skip("requires retained audit PostgreSQL")
	}
	pool, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(401)
			return
		}
		var batch auditBatch
		if json.NewDecoder(r.Body).Decode(&batch) != nil || len(batch.Events) != len(batch.EventIDs) ||
			batch.ProductID != "ledger" || batch.Environment != "delivery-go" || batch.SinkID != "siem" {
			w.WriteHeader(400)
			return
		}
		switch mode.Load() {
		case 0:
			w.WriteHeader(503)
			return
		case 1:
			batch.EventIDs = []string{"unknown"}
		case 2:
			batch.EventIDs = batch.EventIDs[:1]
		case 3:
			if slices.Contains(batch.EventIDs, "z-request") {
				w.WriteHeader(422)
				return
			}
		}
		batch.Events = nil
		_ = json.NewEncoder(w).Encode(batch)
	}))
	defer server.Close()
	validateCustom := func(kind string, version int, attributes map[string]any) bool {
		tier, _ := attributes["tier"].(string)
		return kind == "product.invoice.approved" && version == 1 && len(attributes) == 1 && tier == "standard"
	}
	delivery, err := NewAuditDelivery(pool, AuditDeliveryOptions{ProductID: "ledger", Environment: "delivery-go", SinkID: "siem", Endpoint: server.URL, Token: "test-token", TransportProfile: "loopback", ValidateCustom: validateCustom})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStorage(pool, StorageOptions{ProductID: "ledger", Environment: "delivery-go", AuditSinks: []string{"siem"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../contracts/fixtures/audit-backfill.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Events []map[string]any }
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, event := range fixture.Events[:2] {
		event["environment"] = "delivery-go"
		if _, err := store.AppendAudit(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	due := func() {
		t.Helper()
		if _, err := pool.ExecContext(ctx, "UPDATE heimdall.audit_deliveries SET next_attempt_at=0 WHERE environment='delivery-go' AND state='pending'"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := delivery.Deliver(ctx, 10); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal(err)
	}
	if result, err := delivery.Deliver(ctx, 10); err != nil || result != (AuditDeliveryResult{}) {
		t.Fatal(result, err)
	}
	due()
	mode.Store(1)
	if _, err := delivery.Deliver(ctx, 10); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal(err)
	}
	due()
	mode.Store(3)
	if result, err := delivery.Deliver(ctx, 10); err != nil || result != (AuditDeliveryResult{2, 1, 1}) {
		t.Fatal(result, err)
	}
	if ok, err := delivery.Requeue(ctx, "z-request", 3); err != nil || !ok {
		t.Fatal(ok, err)
	}
	mode.Store(2)
	if result, err := delivery.Deliver(ctx, 10); err != nil || result != (AuditDeliveryResult{1, 1, 0}) {
		t.Fatal(result, err)
	}
	var count int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM heimdall.audit_deliveries WHERE environment='delivery-go' AND state='delivered'").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	writer, err := NewCustomAuditWriter(pool, CustomAuditOptions{
		ProductID: "ledger", Environment: "delivery-go", AuditSinks: []string{"siem"}, ValidateCustom: validateCustom,
	})
	if err != nil {
		t.Fatal(err)
	}
	tenant := "tenant-1"
	event := CustomAuditEvent{
		EventID: "custom-invoice", EventType: "product.invoice.approved", EventVersion: 1,
		OccurredAt: 1000, RecordedAt: 1001, TenantID: &tenant,
		Actor: map[string]any{"kind": "system", "id": "ledger"}, Target: map[string]any{"type": "invoice", "id": "invoice-1"},
		Action: "invoice.approve", Outcome: "succeeded", RequestID: "request-1", Attributes: map[string]any{"tier": "standard"},
	}
	invalid := event
	invalid.EventType = "heimdall.authentication.completed"
	if _, err := writer.Record(ctx, invalid); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	invalid = event
	invalid.EventVersion = 2
	if _, err := writer.Record(ctx, invalid); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	invalid = event
	invalid.Attributes = map[string]any{"tier": "standard", "token": "value"}
	if _, err := writer.Record(ctx, invalid); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	receipt, err := writer.Record(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := writer.Record(ctx, event); err != nil || again != receipt {
		t.Fatal(again, err)
	}
	invalid = event
	invalid.RequestID = "other"
	if _, err := writer.Record(ctx, invalid); !errors.Is(err, contract.ErrConflict) {
		t.Fatal(err)
	}
	if result, err := delivery.Deliver(ctx, 10); err != nil || result != (AuditDeliveryResult{1, 1, 0}) {
		t.Fatal(result, err)
	}
}
