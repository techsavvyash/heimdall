package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"strings"
	"testing"
	"time"
)

type capacityHistoryFixture struct {
	Scopes                            []struct{ ProductID, Environment, Resolution, Language string }
	Events, Decisions, ReservedEvents int64
}

func capacityHistoryData(t *testing.T) capacityHistoryFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-capacity-backfill.json")
	if e != nil {
		t.Fatal(e)
	}
	var f capacityHistoryFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	return f
}
func TestCapacityBackfillInput(t *testing.T) {
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	ctx := context.Background()
	validate := backfillPort(t, db, "ledger").validate
	for _, limit := range []int{-1, 0, 101} {
		if _, e := AdvanceAuditCapacityUpgrade(ctx, db, limit, true, validate); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	for _, args := range []struct {
		ctx      context.Context
		db       *sql.DB
		drained  bool
		validate func(map[string]any) error
	}{{nil, db, true, validate}, {ctx, nil, true, validate}, {ctx, db, false, validate}, {ctx, db, true, nil}} {
		if _, e := AdvanceAuditCapacityUpgrade(args.ctx, args.db, 10, args.drained, args.validate); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := AdvanceAuditCapacityUpgrade(ctx, db, 10, true, validate); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestCapacityBackfillSeed(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CAPACITY_HISTORY_URL")
	if url == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	validator := backfillPort(t, db, "ledger").validate
	mode := os.Getenv("HEIMDALL_TEST_CAPACITY_HISTORY_MODE")
	if mode == "late" {
		f := capacityWritesData(t)
		f.Ordinary["eventId"] = "late"
		if _, e = backfillPort(t, db, "ledger").Append(ctx, f.Ordinary); e != nil {
			t.Fatal(e)
		}
		return
	}
	seed := func(product, environment, resolution string, large bool) {
		t.Helper()
		f := capacityWritesData(t)
		for _, event := range []map[string]any{f.Request, f.Normal, f.Terminal, f.Administration, f.Ordinary} {
			event["productId"] = product
			event["environment"] = environment
		}
		f.Administration["eventId"] = contract.RecoveryEventID(product, environment, f.Terminal["eventId"].(string))
		if large {
			for _, event := range []map[string]any{f.Request, f.Terminal} {
				event["target"] = map[string]any{"type": "document", "id": strings.Repeat("🚀", 8192)}
			}
		}
		a, e := NewAudit(db, product, environment, []string{"siem"}, validator)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = a.AppendDecision(ctx, f.Request); e != nil {
			t.Fatal(e)
		}
		if resolution == "completed" {
			if _, e = a.AppendDecision(ctx, f.Normal); e != nil {
				t.Fatal(e)
			}
		}
		if resolution == "recovered" {
			if _, e = a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff); e != nil {
				t.Fatal(e)
			}
		}
		if !large {
			if _, e = a.Append(ctx, f.Ordinary); e != nil {
				t.Fatal(e)
			}
		}
	}
	if mode == "budget" {
		for i := range 7 {
			seed(fmt.Sprintf("budget-%d", i), "production", "recovered", true)
		}
		f := capacityWritesData(t)
		f.Ordinary["target"] = map[string]any{"type": "document", "id": strings.Repeat("🚀", 8192)}
		f.Ordinary["productId"] = "a-events"
		a := backfillPort(t, db, "a-events")
		for i := range 100 {
			f.Ordinary["eventId"] = fmt.Sprintf("large-%03d", i)
			if _, e = a.Append(ctx, f.Ordinary); e != nil {
				t.Fatal(e)
			}
		}
		return
	}
	for _, s := range capacityHistoryData(t).Scopes {
		if s.Language == "go" {
			seed(s.ProductID, s.Environment, s.Resolution, false)
		}
	}
}
func TestCapacityBackfillPendingRecovery(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CAPACITY_RECOVER_HISTORY_URL")
	if url == "" {
		t.Skip("accounted test database required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	// Harness enables only this isolated policy for a port-level continuation test;
	// no public protocol activation is implied.
	f := capacityWritesData(t)
	a := backfillPort(t, db, "ledger")
	if _, e = a.RecoverCapacity(context.Background(), f.Terminal, f.Administration, f.Cutoff); e != nil {
		t.Fatal(e)
	}
	if _, e = a.RecoverCapacity(context.Background(), f.Terminal, f.Administration, f.Cutoff); e != nil {
		t.Fatal(e)
	}
	var used, reserved int64
	if e = db.QueryRow("SELECT used_events,reserved_events FROM heimdall_capacity.policy").Scan(&used, &reserved); e != nil || used != 13 || reserved != 2 {
		t.Fatal(used, reserved, e)
	}
}

func TestCapacityBackfillLock(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CAPACITY_ACCOUNTED_URL")
	if url == "" {
		t.Skip("accounted database required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = CheckAuditReadiness(context.Background(), db); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal("complete backfill activated v2", e)
	}
	if _, e = backfillPort(t, db, "ledger").AppendCapacity(context.Background(), capacityWritesData(t).Request); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal("complete backfill admitted serving write", e)
	}
	holder, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Rollback()
	if _, e = holder.Exec("SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, e = AdvanceAuditCapacityUpgrade(ctx, db, 100, true, backfillPort(t, db, "ledger").validate)
	cancel()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	holder.Rollback()
	p, e := AdvanceAuditCapacityUpgrade(context.Background(), db, 100, true, backfillPort(t, db, "ledger").validate)
	if e != nil || p.Phase != "complete" || p.Processed != 0 || p.ScannedEvents != 11 || p.ScannedDecisions != 4 {
		t.Fatal(p, e)
	}
}
