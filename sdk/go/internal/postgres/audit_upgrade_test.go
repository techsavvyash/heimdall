package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	neturl "net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func upgradeFiles(t *testing.T) [3][]byte {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-upgrade-prepare.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Files []struct{ Path, Checksum string }
	}
	if json.Unmarshal(raw, &f) != nil || len(f.Files) != 3 {
		t.Fatal("fixture")
	}
	var files [3][]byte
	for i, item := range f.Files {
		if item.Checksum != auditUpgradeChecksums[i] {
			t.Fatal("checksum constant")
		}
		files[i], e = os.ReadFile(filepath.Join("../../../../contracts/persistence/session", item.Path))
		if e != nil {
			t.Fatal(e)
		}
	}
	return files
}
func TestAuditUpgradeInput(t *testing.T) {
	f := upgradeFiles(t)
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	for _, args := range []struct {
		ctx     context.Context
		db      *sql.DB
		drained bool
		bad     bool
	}{{nil, db, true, false}, {context.Background(), nil, true, false}, {context.Background(), db, false, false}, {context.Background(), db, true, true}} {
		first := f[0]
		if args.bad {
			first = append(append([]byte{}, first...), ' ')
		}
		if e := PrepareAuditUpgrade(args.ctx, args.db, first, f[1], f[2], args.drained); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestAuditUpgrade(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_UPGRADE_DATABASE_URL")
	if url == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	f := upgradeFiles(t)
	roURL, _ := neturl.Parse(url)
	q := roURL.Query()
	q.Set("options", "-c default_transaction_read_only=on")
	roURL.RawQuery = q.Encode()
	ro, _ := sql.Open("postgres", roURL.String())
	defer ro.Close()
	if e = PrepareAuditUpgrade(ctx, ro, f[0], f[1], f[2], true); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = db.Exec("CREATE FUNCTION public.reject_upgrade() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN IF to_regclass('heimdall.audit_upgrade') IS NOT NULL THEN RAISE EXCEPTION 'test upgrade DDL failure'; END IF; END $$; CREATE EVENT TRIGGER reject_upgrade ON ddl_command_end WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION public.reject_upgrade()"); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	var clean bool
	if e = db.QueryRow("SELECT to_regclass('heimdall.audit_decisions') IS NULL AND to_regclass('heimdall.audit_decision_backfill') IS NULL AND to_regclass('heimdall.audit_upgrade') IS NULL AND (SELECT state='ready' FROM heimdall.schema_metadata)").Scan(&clean); e != nil || !clean {
		t.Fatal("partial DDL", e)
	}
	if _, e = db.Exec("DROP EVENT TRIGGER reject_upgrade; DROP FUNCTION public.reject_upgrade()"); e != nil {
		t.Fatal(e)
	}
	lock, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock.Exec("SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		t.Fatal(e)
	}
	waiting, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	e = PrepareAuditUpgrade(waiting, db, f[0], f[1], f[2], true)
	cancel()
	lock.Rollback()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	if e = CheckReadiness(ctx, db); e != nil {
		t.Fatal("cancelled preparation changed readiness", e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true) }()
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if e = <-errs; e != nil {
			t.Fatal(e)
		}
	}
	var before, after string
	if e = db.QueryRow("SELECT prepared_at::text FROM heimdall.audit_upgrade").Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow("SELECT prepared_at::text FROM heimdall.audit_upgrade").Scan(&after); e != nil || before != after {
		t.Fatal("receipt rewritten", e)
	}
	if e = CheckReadiness(ctx, db); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	var state string
	var version, ledger, checkpoints int
	if e = db.QueryRow("SELECT state,database_version,(SELECT count(*) FROM heimdall.migrations),(SELECT count(*) FROM heimdall.audit_decision_backfill) FROM heimdall.schema_metadata").Scan(&state, &version, &ledger, &checkpoints); e != nil || state != "migrating" || version != 1 || ledger != 1 || checkpoints != 0 {
		t.Fatal(state, version, ledger, checkpoints, e)
	}
	if _, e = db.Exec("UPDATE heimdall.audit_upgrade SET decision_checksum=repeat('0',64)"); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditUpgrade(ctx, db, f[0], f[1], f[2], true); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE heimdall.audit_upgrade SET decision_checksum=$1", auditUpgradeChecksums[0]); e != nil {
		t.Fatal(e)
	}
	partial, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_UPGRADE_PARTIAL_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer partial.Close()
	if _, e = partial.Exec(string(f[0])); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditUpgrade(ctx, partial, f[0], f[1], f[2], true); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if e = partial.QueryRow("SELECT to_regclass('heimdall.audit_decision_backfill') IS NULL AND to_regclass('heimdall.audit_upgrade') IS NULL AND (SELECT state='ready' FROM heimdall.schema_metadata)").Scan(&clean); e != nil || !clean {
		t.Fatal("partial adopted", e)
	}
}
func TestAuditUpgradeLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_UPGRADE_LOSS")
	if url == "" {
		t.Skip("fault proxy required")
	}
	db, _ := sql.Open("postgres", url)
	defer db.Close()
	f := upgradeFiles(t)
	if e := PrepareAuditUpgrade(context.Background(), db, f[0], f[1], f[2], true); !errors.Is(e, contract.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	direct, _ := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_UPGRADE_LOSS_DIRECT"))
	defer direct.Close()
	if e := PrepareAuditUpgrade(context.Background(), direct, f[0], f[1], f[2], true); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := direct.QueryRow("SELECT count(*) FROM heimdall.audit_upgrade").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
