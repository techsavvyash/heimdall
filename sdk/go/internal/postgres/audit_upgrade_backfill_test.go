package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	neturl "net/url"
	"os"
	"sync"
	"testing"
	"time"
)

type upgradeScopesFixture struct {
	Limit                             int
	Scopes                            []struct{ ProductID, Environment string }
	EventsPerScope, DecisionsPerScope int
}

func upgradeScopeData(t *testing.T) upgradeScopesFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-upgrade-scopes.json")
	if e != nil {
		t.Fatal(e)
	}
	var f upgradeScopesFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	return f
}
func coordinatorSeed(t *testing.T, db *sql.DB) {
	t.Helper()
	validate := backfillPort(t, db, "validator").validate
	for _, scope := range upgradeScopeData(t).Scopes {
		a, e := NewAudit(db, scope.ProductID, scope.Environment, []string{"siem"}, validate)
		if e != nil {
			t.Fatal(e)
		}
		for _, event := range backfillData(t).Events {
			event["productId"] = scope.ProductID
			event["environment"] = scope.Environment
			if _, e = a.Append(context.Background(), event); e != nil {
				t.Fatal(e)
			}
		}
	}
}
func coordinatorPrepare(t *testing.T, db *sql.DB) {
	t.Helper()
	files := upgradeFiles(t)
	if e := PrepareAuditUpgrade(context.Background(), db, files[0], files[1], files[2], true); e != nil {
		t.Fatal(e)
	}
}
func TestAuditCoordinatorInput(t *testing.T) {
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	validate := backfillPort(t, db, "validator").validate
	ctx := context.Background()
	for _, limit := range []int{0, 101} {
		if _, e := AdvanceAuditUpgrade(ctx, db, limit, true, validate); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	if _, e := AdvanceAuditUpgrade(ctx, db, 1, false, validate); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if _, e := AdvanceAuditUpgrade(ctx, db, 1, true, nil); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := AdvanceAuditUpgrade(cancelled, db, 1, true, validate); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestAuditCoordinator(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_COORDINATE_DATABASE_URL")
	if url == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	f := upgradeScopeData(t)
	validate := backfillPort(t, db, "validator").validate
	coordinatorPrepare(t, db)
	empty, e := AdvanceAuditUpgrade(ctx, db, f.Limit, true, validate)
	if e != nil || empty.Status != "coverage_complete" {
		t.Fatal(empty, e)
	}
	coordinatorSeed(t, db)
	roURL, _ := neturl.Parse(url)
	q := roURL.Query()
	q.Set("options", "-c default_transaction_read_only=on")
	roURL.RawQuery = q.Encode()
	ro, _ := sql.Open("postgres", roURL.String())
	defer ro.Close()
	if _, e = AdvanceAuditUpgrade(ctx, ro, f.Limit, true, validate); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	lock, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock.Exec("SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = AdvanceAuditUpgrade(deadline, db, f.Limit, true, validate)
	cancel()
	lock.Rollback()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall.audit_upgrade SET decision_checksum=repeat('0',64)"); e != nil {
		t.Fatal(e)
	}
	if _, e = AdvanceAuditUpgrade(ctx, db, f.Limit, true, validate); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall.audit_upgrade SET decision_checksum=$1", auditUpgradeChecksums[0]); e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	for i := 0; i < 2; i++ {
		step, e := AdvanceAuditUpgrade(ctx, db, f.Limit, true, validate)
		if e != nil || step.Status != "registered" || step.Registered != 2 {
			t.Fatal(step, e)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempts := 0; attempts < 100; attempts++ {
				step, e := AdvanceAuditUpgrade(ctx, db, f.Limit, true, validate)
				if e != nil {
					errs <- e
					return
				}
				if step.Status == "coverage_complete" {
					errs <- nil
					return
				}
			}
			errs <- errors.New("no progress")
		}()
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		if e = <-errs; e != nil {
			t.Fatal(e)
		}
	}
	var scopes, decisions, events, deliveries int
	if e = db.QueryRow("SELECT (SELECT count(*) FROM heimdall.audit_decision_backfill WHERE phase='complete' AND expected_count=4 AND scanned_count=4),(SELECT count(*) FROM heimdall.audit_decisions),(SELECT count(*) FROM heimdall.audit_events),(SELECT count(*) FROM heimdall.audit_deliveries)").Scan(&scopes, &decisions, &events, &deliveries); e != nil || scopes != len(f.Scopes) || decisions != scopes*f.DecisionsPerScope || events != scopes*f.EventsPerScope || deliveries != events {
		t.Fatal(scopes, decisions, events, deliveries, e)
	}
	// Inject a post-sweep old-writer violation; completed checkpoints must not be reset.
	extra := backfillData(t).Events[3]
	extra["eventId"] = "late-policy"
	extra["productId"] = "ledger"
	if _, e = backfillPort(t, db, "ledger").Append(ctx, extra); e != nil {
		t.Fatal(e)
	}
	if _, e = AdvanceAuditUpgrade(ctx, db, f.Limit, true, validate); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	bad, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_COORDINATE_BAD_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer bad.Close()
	coordinatorSeed(t, bad)
	coordinatorPrepare(t, bad)
	if _, e = bad.Exec("UPDATE heimdall.audit_events SET payload_digest=decode(repeat('b',64),'hex') WHERE product_id='ledger' AND environment='production'"); e != nil {
		t.Fatal(e)
	}
	if _, e = AdvanceAuditUpgrade(ctx, bad, 100, true, validate); e != nil {
		t.Fatal(e)
	}
	step, e := AdvanceAuditUpgrade(ctx, bad, 100, true, validate)
	if !errors.Is(e, contract.ErrAuditUnavailable) || step == nil || step.Status != "backfill_failed" || step.ProductID != "ledger" || step.Environment != "production" {
		t.Fatal(step, e)
	}
	var n int
	if e = bad.QueryRow("SELECT count(*) FROM heimdall.audit_decisions").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	cli, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_COORDINATE_CLI_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer cli.Close()
	coordinatorSeed(t, cli)
	loss, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_COORDINATE_LOSS_DIRECT"))
	if e != nil {
		t.Fatal(e)
	}
	defer loss.Close()
	coordinatorSeed(t, loss)
	coordinatorPrepare(t, loss)
	custom, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_COORDINATE_CUSTOM_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer custom.Close()
	customPort, e := NewAudit(custom, "custom-product", "production", nil, func(event map[string]any) error {
		if e := contract.ValidateAuditShape(event); e != nil {
			return e
		}
		raw, _ := json.Marshal(event)
		var typed contract.AuditEvent
		if json.Unmarshal(raw, &typed) != nil {
			return contract.ErrInvalidArgument
		}
		return contract.ValidateAuditEvent(typed, "product", func(name string, version int, attributes map[string]any) bool {
			return name == "product.history_imported" && version == 1 && len(attributes) == 0
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	customEvent := backfillData(t).Events[3]
	customEvent["productId"] = "custom-product"
	customEvent["eventType"] = "product.history_imported"
	customEvent["source"] = "product"
	if _, e = customPort.Append(ctx, customEvent); e != nil {
		t.Fatal(e)
	}
	coordinatorPrepare(t, custom)

}
func TestAuditCoordinatorLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_COORDINATE_LOSS")
	if url == "" {
		t.Skip("fault proxy required")
	}
	db, _ := sql.Open("postgres", url)
	defer db.Close()
	direct, _ := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_COORDINATE_LOSS_DIRECT"))
	defer direct.Close()
	validate := backfillPort(t, direct, "validator").validate
	ctx := context.Background()
	if _, e := AdvanceAuditUpgrade(ctx, db, 2, true, validate); !errors.Is(e, contract.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	var registered int
	if e := direct.QueryRow("SELECT count(*) FROM heimdall.audit_decision_backfill").Scan(&registered); e != nil || registered != 2 {
		t.Fatal(registered, e)
	}
	for i := 0; i < 100; i++ {
		step, e := AdvanceAuditUpgrade(ctx, direct, 100, true, validate)
		if e != nil {
			t.Fatal(e)
		}
		if step.Status == "coverage_complete" {
			return
		}
	}
	t.Fatal("did not finish")
}
