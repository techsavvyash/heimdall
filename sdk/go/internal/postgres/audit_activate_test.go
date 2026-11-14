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
	"sync"
	"testing"
	"time"
)

type activationFixture struct {
	HistoryCorruption []struct{ Name, SQL string }
	Readiness         []struct {
		Name  string
		SQL   []string
		Ready bool
	}
}

func activationData(t *testing.T) activationFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-activation.json")
	if e != nil {
		t.Fatal(e)
	}
	var f activationFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	return f
}
func activationPrepared(t *testing.T, db *sql.DB) {
	t.Helper()
	coordinatorSeed(t, db)
	coordinatorPrepare(t, db)
	validate := backfillPort(t, db, "validator").validate
	for i := 0; i < 100; i++ {
		step, e := AdvanceAuditUpgrade(context.Background(), db, 2, true, validate)
		if e != nil {
			t.Fatal(e)
		}
		if step.Status == "coverage_complete" {
			return
		}
	}
	t.Fatal("backfill did not finish")
}
func TestAuditActivationInput(t *testing.T) {
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	ctx := context.Background()
	validate := func(map[string]any) error { return nil }
	for _, e := range []error{ActivateAuditUpgrade(nil, db, true, validate), ActivateAuditUpgrade(ctx, nil, true, validate), ActivateAuditUpgrade(ctx, db, false, validate), ActivateAuditUpgrade(ctx, db, true, nil)} {
		if !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if e := ActivateAuditUpgrade(cancelCtx, db, true, validate); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if e := CheckAuditReadiness(cancelCtx, db); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestAuditActivation(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_ACTIVATION_URL")
	if dsn == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	validate := backfillPort(t, db, "validator").validate
	coordinatorSeed(t, db)
	large := backfillPort(t, db, "ledger")
	for i := 0; i < 120; i++ {
		event := backfillData(t).Events[3]
		event["eventId"] = fmt.Sprintf("large-%03d", i)
		event["target"] = map[string]any{"type": "document", "id": strings.Repeat("🚀", 8192)}
		if _, e = large.Append(ctx, event); e != nil {
			t.Fatal(e)
		}
	}
	coordinatorPrepare(t, db)
	if e = ActivateAuditUpgrade(ctx, db, true, validate); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal("incomplete coverage", e)
	}
	for i := 0; i < 100; i++ {
		step, e := AdvanceAuditUpgrade(ctx, db, 100, true, validate)
		if e != nil {
			t.Fatal(e)
		}
		if step.Status == "coverage_complete" {
			break
		}
	}
	seen := map[string]bool{}
	inspect, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	e = validateUpgradeHistory(ctx, db, inspect, func(event map[string]any) error {
		seen[fmt.Sprint(event["productId"], "/", event["environment"], "/", event["eventId"])] = true
		return validate(event)
	})
	inspect.Rollback()
	if e != nil || len(seen) != 136 {
		t.Fatal("bounded pages missed history", len(seen), e)
	}
	var bytes int64
	if e = db.QueryRow("SELECT sum(octet_length(event::text)) FROM (SELECT event FROM heimdall.audit_events ORDER BY product_id COLLATE \"C\",environment COLLATE \"C\",event_id COLLATE \"C\" LIMIT 100) first_page").Scan(&bytes); e != nil || bytes <= 4194304 {
		t.Fatal("byte-bound fixture too small", bytes, e)
	}
	abort, abortNow := context.WithCancel(ctx)
	e = ActivateAuditUpgrade(abort, db, true, func(event map[string]any) error { abortNow(); return validate(event) })
	abortNow()
	if !errors.Is(e, contract.ErrCancelled) {
		t.Fatal("scan cancellation", e)
	}
	ro, e := sql.Open("postgres", dsn+"&options=-c%20default_transaction_read_only%3Don")
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	if e = ActivateAuditUpgrade(ctx, ro, true, validate); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal("read-only activation", e)
	}
	for _, c := range activationData(t).HistoryCorruption {
		t.Run(c.Name, func(t *testing.T) {
			tx, e := db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			if _, e = tx.Exec(c.SQL); e != nil {
				t.Fatal(e)
			}
			if e = validateUpgradeHistory(ctx, db, tx, validate); !errors.Is(e, contract.ErrAuditUnavailable) {
				t.Fatal(e)
			}
		})
	}
	// The full activation gate must reject a persisted missing slot even though all
	// event counts/checkpoints and schema remain unchanged.
	if _, e = db.Exec("CREATE TABLE public.saved_pending AS SELECT * FROM heimdall.audit_decisions WHERE request_event_id='m-pending'; DELETE FROM heimdall.audit_decisions WHERE request_event_id='m-pending'"); e != nil {
		t.Fatal(e)
	}
	if e = ActivateAuditUpgrade(ctx, db, true, validate); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	var untouched bool
	if e = db.QueryRow("SELECT (SELECT state='migrating' AND database_version=1 FROM heimdall.schema_metadata) AND (SELECT count(*) FROM heimdall.migrations)=1").Scan(&untouched); e != nil || !untouched {
		t.Fatal("partial publication", e)
	}
	if _, e = db.Exec("INSERT INTO heimdall.audit_decisions SELECT * FROM public.saved_pending; DROP TABLE public.saved_pending"); e != nil {
		t.Fatal(e)
	}
	lock, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock.Exec("SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		t.Fatal(e)
	}
	cancelled, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	e = ActivateAuditUpgrade(cancelled, db, true, validate)
	stop()
	lock.Rollback()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- ActivateAuditUpgrade(ctx, db, true, validate) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = CheckAuditReadiness(ctx, db); e != nil {
		t.Fatal(e)
	}
	if e = CheckAuditReadiness(ctx, ro); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal("read-only readiness", e)
	}
	if e = CheckReadiness(ctx, db); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal("old reader accepted v2", e)
	}
	// An activated retry is stable and does not rerun stale historical counters.
	a := backfillPort(t, db, "ledger")
	event := backfillData(t).Events[3]
	event["eventId"] = "after-activation"
	if _, e = a.Append(ctx, event); e != nil {
		t.Fatal(e)
	}
	if e = ActivateAuditUpgrade(ctx, db, true, validate); e != nil {
		t.Fatal(e)
	}
	for _, c := range activationData(t).Readiness {
		t.Run(c.Name, func(t *testing.T) {
			tx, e := db.Begin()
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			for _, s := range c.SQL {
				if _, e = tx.Exec(s); e != nil {
					t.Fatal(e)
				}
			}
			var ready bool
			if e = tx.QueryRow(auditReadinessSQL, sessionChecksum, decisionChecksum).Scan(&ready); e != nil || ready != c.Ready {
				t.Fatal(ready, e)
			}
		})
	}
	for _, key := range []string{"HEIMDALL_TEST_ACTIVATION_LOSS_DIRECT", "HEIMDALL_TEST_ACTIVATION_CLI"} {
		other, e := sql.Open("postgres", os.Getenv(key))
		if e != nil {
			t.Fatal(e)
		}
		activationPrepared(t, other)
		other.Close()
	}
}
func TestAuditActivationLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_ACTIVATION_LOSS")
	if url == "" {
		t.Skip("commit proxy required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	validate := backfillPort(t, db, "validator").validate
	if e = ActivateAuditUpgrade(ctx, db, true, validate); !errors.Is(e, contract.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	direct, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_ACTIVATION_LOSS_DIRECT"))
	if e != nil {
		t.Fatal(e)
	}
	defer direct.Close()
	if e = CheckAuditReadiness(ctx, direct); e != nil {
		t.Fatal(e)
	}
	var before, after string
	if e = direct.QueryRow("SELECT applied_at::text FROM heimdall.migrations WHERE version=2").Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = ActivateAuditUpgrade(ctx, direct, true, validate); e != nil {
		t.Fatal(e)
	}
	if e = direct.QueryRow("SELECT applied_at::text FROM heimdall.migrations WHERE version=2").Scan(&after); e != nil || before != after {
		t.Fatal("changed activation receipt", e)
	}
}
