package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"slices"
	"testing"
)

type capacityWritesFixture struct {
	Request, Terminal, Administration, Normal, Ordinary map[string]any
	Cutoff                                              int64
	Scenarios                                           []string
}

func capacityWritesData(t *testing.T) capacityWritesFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-capacity-writes.json")
	if e != nil {
		t.Fatal(e)
	}
	var f capacityWritesFixture
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	return f
}
func TestAuditCapacityInput(t *testing.T) {
	f := capacityWritesData(t)
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	a := backfillPort(t, db, "ledger")
	ctx := context.Background()
	for _, event := range []map[string]any{f.Terminal, {}} {
		if _, e := a.AppendCapacity(ctx, event); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	f.Administration["eventId"] = "wrong"
	if _, e := a.RecoverCapacity(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	f = capacityWritesData(t)
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := a.AppendCapacity(ctx, f.Request); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if _, e := a.RecoverCapacity(ctx, f.Terminal, f.Administration, f.Cutoff); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestAuditCapacityLive(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CAPACITY_URL")
	if url == "" {
		t.Skip("live PostgreSQL required")
	}
	mode := os.Getenv("HEIMDALL_TEST_CAPACITY_MODE")
	f := capacityWritesData(t)
	if !slices.Contains(f.Scenarios, mode) {
		t.Fatal("unknown scenario")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	loss := os.Getenv("HEIMDALL_TEST_CAPACITY_LOSS")
	if loss == "" {
		loss = url
	}
	fault, e := sql.Open("postgres", loss)
	if e != nil {
		t.Fatal(e)
	}
	defer fault.Close()
	a := backfillPort(t, db, "ledger")
	lost := backfillPort(t, fault, "ledger")
	ctx := context.Background()
	require := func(e, want error) {
		t.Helper()
		if !errors.Is(e, want) {
			t.Fatalf("got %v want %v", e, want)
		}
	}
	appendEvent := func(port *Audit, event map[string]any, want error) contract.AuditReceipt {
		t.Helper()
		r, e := port.AppendCapacity(ctx, event)
		require(e, want)
		return r
	}
	recoverEvent := func(port *Audit, want error) *AuditRecoveryReceipt {
		t.Helper()
		r, e := port.RecoverCapacity(ctx, f.Terminal, f.Administration, f.Cutoff)
		require(e, want)
		return r
	}
	counters := func(used, reserved int) {
		t.Helper()
		var u, r, n int
		var bytes, deliveries bool
		e := db.QueryRow("SELECT used_events,reserved_events,used_bytes=(SELECT coalesce(sum(envelope_bytes),0) FROM heimdall_capacity.event_charges),used_deliveries=(SELECT coalesce(sum(delivery_rows),0) FROM heimdall_capacity.event_charges) FROM heimdall_capacity.policy").Scan(&u, &r, &bytes, &deliveries)
		if e != nil || u != used || r != reserved || !bytes || !deliveries {
			t.Fatal(u, r, bytes, deliveries, e)
		}
		if e = db.QueryRow("SELECT count(*) FROM heimdall.audit_events").Scan(&n); e != nil || n != used {
			t.Fatal(n, e)
		}
	}
	if slices.Contains([]string{"preparing", "bytes", "deliveries"}, mode) {
		appendEvent(a, f.Request, contract.ErrAuditUnavailable)
		counters(0, 0)
		return
	}
	if mode == "admission" {
		results := make(chan error, 8)
		for i := range 8 {
			product := "ledger"
			if i%2 == 0 {
				product = "support"
			}
			writer := backfillPort(t, db, product)
			event := map[string]any{}
			for k, v := range f.Request {
				event[k] = v
			}
			event["productId"] = product
			event["eventId"] = fmt.Sprintf("request-%d", i)
			event["decisionId"] = fmt.Sprintf("decision-%d", i)
			go func() { _, e := writer.AppendCapacity(ctx, event); results <- e }()
		}
		wins := 0
		for range 8 {
			e := <-results
			if e == nil {
				wins++
			} else {
				require(e, contract.ErrAuditUnavailable)
			}
		}
		if wins != 1 {
			t.Fatal(wins)
		}
		counters(1, 2)
		return
	}
	if mode == "legacy" {
		_, e = a.AppendDecision(ctx, f.Request)
		require(e, nil)
		appendEvent(a, f.Request, contract.ErrAuditUnavailable)
		appendEvent(a, f.Normal, contract.ErrAuditUnavailable)
		var used, reserved, n int
		e = db.QueryRow("SELECT used_events,reserved_events,(SELECT count(*) FROM heimdall.audit_events) FROM heimdall_capacity.policy").Scan(&used, &reserved, &n)
		if e != nil || used != 0 || reserved != 0 || n != 1 {
			t.Fatal(used, reserved, n, e)
		}
		return
	}
	if mode == "loss_ordinary" {
		appendEvent(lost, f.Ordinary, contract.ErrOutcomeUnknown)
		appendEvent(a, f.Ordinary, nil)
		counters(1, 0)
		return
	}
	if mode != "peer_recovery" {
		if mode == "loss_request" {
			appendEvent(lost, f.Request, contract.ErrOutcomeUnknown)
		}
		receipt := appendEvent(a, f.Request, nil)
		if again := appendEvent(a, f.Request, nil); !reflect.DeepEqual(receipt, again) {
			t.Fatal("receipt changed")
		}
		counters(1, 2)
		different, e := NewAudit(db, "ledger", "production", []string{"different"}, a.validate)
		require(e, nil)
		if again := appendEvent(different, f.Request, nil); !reflect.DeepEqual(receipt, again) {
			t.Fatal("sink retry changed receipt")
		}
		changed := map[string]any{}
		for k, v := range f.Request {
			changed[k] = v
		}
		changed["requestId"] = "changed"
		appendEvent(a, changed, contract.ErrConflict)
		appendEvent(a, f.Ordinary, contract.ErrAuditUnavailable)
		counters(1, 2)
	}
	if mode == "sink_failure" {
		recoverEvent(a, contract.ErrUnavailable)
		counters(1, 2)
		var resolution string
		e = db.QueryRow("SELECT resolution FROM heimdall.audit_decisions").Scan(&resolution)
		if e != nil || resolution != "pending" {
			t.Fatal(resolution, e)
		}
		return
	}
	if mode == "peer_request" {
		return
	}
	if mode == "race" {
		results := make(chan error, 2)
		go func() { _, e := a.AppendCapacity(ctx, f.Normal); results <- e }()
		go func() { _, e := a.RecoverCapacity(ctx, f.Terminal, f.Administration, f.Cutoff); results <- e }()
		wins := 0
		for range 2 {
			e := <-results
			if e == nil {
				wins++
			} else {
				require(e, contract.ErrConflict)
			}
		}
		if wins != 1 {
			t.Fatal(wins)
		}
		var resolution string
		if e = db.QueryRow("SELECT resolution FROM heimdall.audit_decisions").Scan(&resolution); e != nil {
			t.Fatal(e)
		}
		n := 2
		if resolution == "recovered" {
			n = 3
		}
		counters(n, 0)
		return
	}
	if slices.Contains([]string{"recovery", "loss_recovery", "peer_recovery"}, mode) {
		if mode == "loss_recovery" {
			recoverEvent(lost, contract.ErrOutcomeUnknown)
		}
		receipt := recoverEvent(a, nil)
		if again := recoverEvent(a, nil); !reflect.DeepEqual(receipt, again) {
			t.Fatal("recovery receipt changed")
		}
		appendEvent(a, f.Normal, contract.ErrConflict)
		f.Administration["requestId"] = "changed"
		recoverEvent(a, contract.ErrConflict)
		appendEvent(a, f.Ordinary, contract.ErrAuditUnavailable)
		counters(3, 0)
	} else {
		if mode == "loss_completion" {
			appendEvent(lost, f.Normal, contract.ErrOutcomeUnknown)
		}
		receipt := appendEvent(a, f.Normal, nil)
		if again := appendEvent(a, f.Normal, nil); !reflect.DeepEqual(receipt, again) {
			t.Fatal("completion receipt changed")
		}
		appendEvent(a, f.Request, nil)
		counters(2, 0)
		recoverEvent(a, contract.ErrConflict)
		appendEvent(a, f.Ordinary, nil)
		counters(3, 0)
	}
}
