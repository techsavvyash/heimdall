package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

func TestIdentityCollectorPostgres(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_AUDIT_DELIVERY_DB")
	if dsn == "" {
		t.Skip("requires retained audit PostgreSQL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	data, err := os.ReadFile("../../../contracts/fixtures/identity-collector.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		NextMicros, UntilMicros int64
		Events                  json.RawMessage
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var outage atomic.Bool
	outage.Store(true)
	var reads atomic.Int32
	var beforeReply atomic.Pointer[func() error]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if outage.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if prepare := beforeReply.Load(); prepare != nil {
			if err := (*prepare)(); err != nil {
				t.Error("mapping switch during scan failed", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte("{}"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]json.RawMessage{"events": fixture.Events})
	}))
	defer server.Close()
	o := IdentityCollectorOptions{
		IdentityEventReaderOptions: provisioning.IdentityEventReaderOptions{
			Issuer: server.URL, OrganizationID: "org", Token: "token", TransportProfile: "loopback",
		},
		ProductID: "ledger", Environment: "collector-go", TenantID: "tenant-a", AuditSinks: []string{"siem"},
		StartMicros: fixture.NextMicros, Aggregates: []IdentityAggregate{{"user", "assigned"}},
	}
	collector, err := NewIdentityCollector(db, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	coverage := IdentityEventCoverage{VisibleBeforeMicros: fixture.UntilMicros, ValidUntilMs: time.Now().Add(15 * time.Second).UnixMilli()}
	gap := coverage
	gap.RetainedFromMicros = fixture.NextMicros
	if _, err := collector.Collect(ctx, gap); !errors.Is(err, contract.ErrUnavailable) || reads.Load() != 0 {
		t.Fatal("retention gap was not blocked", err)
	}
	if _, err := collector.Collect(ctx, coverage); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal("outage was not reported", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM heimdall_directory.event_checkpoints WHERE environment='collector-go'").Scan(&count); err != nil || count != 0 {
		t.Fatal("outage advanced checkpoint", err)
	}
	outage.Store(false)
	broken := o
	broken.AuditSinks = []string{"inject-failure"}
	failing, err := NewIdentityCollector(db, broken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Collect(ctx, coverage); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal("injected outbox failure was not reported", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM heimdall_directory.event_checkpoints WHERE environment='collector-go') + (SELECT count(*) FROM heimdall.audit_events WHERE environment='collector-go')").Scan(&count); err != nil || count != 0 {
		t.Fatal("outbox failure committed partial state", err)
	}
	if result, err := collector.Collect(ctx, coverage); err != nil || result != (IdentityCollection{fixture.UntilMicros, 1}) {
		t.Fatal("collection failed", result, err)
	}
	restarted, err := NewIdentityCollector(db, o)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := restarted.Collect(ctx, coverage); err != nil || result != (IdentityCollection{fixture.UntilMicros, 0}) || reads.Load() != 3 {
		t.Fatal("restart replayed completed window", result, err)
	}
	o.Aggregates = []IdentityAggregate{{"user", "unassigned"}}
	changed, err := NewIdentityCollector(db, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changed.Collect(ctx, coverage); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("changed routing resumed checkpoint", err)
	}
	before, err := collector.Checkpoint(ctx)
	if err != nil || before == nil || before.NextMicros != fixture.UntilMicros || before.MappingRevision != 0 {
		t.Fatal("unexpected initial checkpoint", before, err)
	}
	stale := *before
	stale.NextMicros--
	if _, err := changed.ActivateMapping(ctx, stale); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("stale boundary accepted", err)
	}
	after, err := changed.ActivateMapping(ctx, *before)
	if err != nil || after.NextMicros != before.NextMicros || after.MappingRevision != 1 || after.MappingDigest == before.MappingDigest {
		t.Fatal("activation did not preserve the boundary", after, err)
	}
	if retry, err := changed.ActivateMapping(ctx, *before); err != nil || retry != after {
		t.Fatal("activation replay failed", retry, err)
	}
	if _, err := collector.Collect(ctx, coverage); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("old mapping still active", err)
	}
	if result, err := changed.Collect(ctx, coverage); err != nil || result != (IdentityCollection{fixture.UntilMicros, 0}) {
		t.Fatal("new mapping rewound the checkpoint", result, err)
	}
	reverted, err := collector.ActivateMapping(ctx, after)
	if err != nil || reverted.MappingRevision != 2 || reverted.MappingDigest != before.MappingDigest || reverted.NextMicros != before.NextMicros {
		t.Fatal("mapping rollback did not preserve the boundary", reverted, err)
	}
	if _, err := changed.ActivateMapping(ctx, *before); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("stale revision accepted after reverting mapping", err)
	}
	switchTwice := func() error {
		temporary, err := changed.ActivateMapping(ctx, reverted)
		if err != nil {
			return err
		}
		_, err = collector.ActivateMapping(ctx, temporary)
		return err
	}
	beforeReply.Store(&switchTwice)
	later := coverage
	later.VisibleBeforeMicros++
	if _, err := collector.Collect(ctx, later); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("in-flight scan committed after mapping changed twice", err)
	}
	if current, err := collector.Checkpoint(ctx); err != nil || current == nil ||
		*current != (IdentityCollectorCheckpoint{before.NextMicros, before.MappingDigest, 4}) {
		t.Fatal("stale scan advanced checkpoint", current, err)
	}
	var payload string
	if err := db.QueryRowContext(ctx, "SELECT event->>'payload' FROM heimdall.audit_events WHERE environment='collector-go'").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Source, TenantID string
		Target           struct{ ID string }
		Attributes       map[string]string
	}
	if json.Unmarshal(raw, &observed) != nil || observed.Source != "zitadel" || observed.TenantID != "tenant-a" ||
		observed.Target.ID != "assigned" || observed.Attributes["sequence"] != "18446744073709551615" ||
		strings.Contains(string(raw), "discard") {
		t.Fatal("unexpected retained metadata")
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM heimdall.audit_deliveries WHERE environment='collector-go' AND sink_id='siem'").Scan(&count); err != nil || count != 1 {
		t.Fatal("missing delivery row", err)
	}
}

func TestIdentityCollectorCommitLoss(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_COLLECTOR_COMMIT_LOSS")
	if dsn == "" {
		t.Skip("requires collector COMMIT response-loss proxy")
	}
	fault, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Close()
	fault.SetMaxOpenConns(1)
	db, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_AUDIT_DELIVERY_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	data, err := os.ReadFile("../../../contracts/fixtures/identity-collector.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		NextMicros, UntilMicros int64
		Events                  json.RawMessage
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var reads, deliveries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin/v1/events/_search":
			reads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]json.RawMessage{"events": fixture.Events})
		case "/audit":
			deliveries.Add(1)
			var batch auditBatch
			if r.Header.Get("Authorization") != "Bearer sink-token" ||
				json.NewDecoder(r.Body).Decode(&batch) != nil || batch.ProductID != "ledger" ||
				batch.Environment != "collector-loss-go" || batch.SinkID != "siem" ||
				len(batch.EventIDs) != 1 || len(batch.Events) != 1 {
				t.Error("unexpected batch scope")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			event := batch.Events[0]
			attributes, _ := event["attributes"].(map[string]any)
			target, _ := event["target"].(map[string]any)
			if event["eventId"] != batch.EventIDs[0] || event["source"] != "zitadel" ||
				event["tenantId"] != "tenant-a" || attributes["sequence"] != "18446744073709551615" ||
				target["type"] != "zitadel_aggregate" || target["id"] != "assigned" {
				t.Error("unexpected imported event")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			batch.Events = nil
			_ = json.NewEncoder(w).Encode(batch)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	o := IdentityCollectorOptions{
		IdentityEventReaderOptions: provisioning.IdentityEventReaderOptions{
			Issuer: server.URL, OrganizationID: "org", Token: "provider-token", TransportProfile: "loopback",
		},
		ProductID: "ledger", Environment: "collector-loss-go", TenantID: "tenant-a",
		AuditSinks: []string{"siem"}, StartMicros: fixture.NextMicros,
		Aggregates: []IdentityAggregate{{"user", "assigned"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	coverage := IdentityEventCoverage{VisibleBeforeMicros: fixture.UntilMicros, ValidUntilMs: time.Now().Add(15 * time.Second).UnixMilli()}
	collector, err := NewIdentityCollector(fault, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Collect(ctx, coverage); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal("loss of commit response must be uncertain", err)
	}
	restarted, err := NewIdentityCollector(db, o)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := restarted.Collect(ctx, coverage); err != nil || result != (IdentityCollection{fixture.UntilMicros, 0}) || reads.Load() != 1 {
		t.Fatal("restart did not resume committed checkpoint", result, err)
	}
	delivery, err := NewAuditDelivery(db, AuditDeliveryOptions{
		ProductID: o.ProductID, Environment: o.Environment, SinkID: "siem", Endpoint: server.URL + "/audit",
		Token: "sink-token", TransportProfile: "loopback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := delivery.Deliver(ctx, 10); err != nil || result.Delivered != 1 || result.Quarantined != 0 {
		t.Fatal("imported event was not delivered", result, err)
	}
	if result, err := delivery.Deliver(ctx, 10); err != nil || result.Delivered != 0 || deliveries.Load() != 1 {
		t.Fatal("acknowledged event was redelivered", result, err)
	}
}
