package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type capacityUpgradeFixture struct {
	Files                                []struct{ Path, Checksum string }
	SchemaSQLChecksum, SignatureChecksum string
	Limits                               struct{ Events, EnvelopeBytes, Deliveries int64 }
	Cases                                []struct{ Name, SQL string }
}

func capacityUpgradeData(t *testing.T) (capacityUpgradeFixture, [2][]byte, contract.AuditCapacity) {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-capacity-upgrade.json")
	if e != nil {
		t.Fatal(e)
	}
	var f capacityUpgradeFixture
	if json.Unmarshal(raw, &f) != nil || len(f.Files) != 2 {
		t.Fatal("fixture")
	}
	var files [2][]byte
	for i, item := range f.Files {
		files[i], e = os.ReadFile(filepath.Join("../../../../contracts/persistence/session", item.Path))
		if e != nil {
			t.Fatal(e)
		}
		digest := sha256.Sum256(files[i])
		if hex.EncodeToString(digest[:]) != item.Checksum || item.Checksum != capacityUpgradeChecksums[i] {
			t.Fatal("SQL checksum")
		}
	}
	for _, pair := range [][2]string{{auditCapacitySchemaSQL, f.SchemaSQLChecksum}, {auditCapacitySchemaJSON, f.SignatureChecksum}} {
		digest := sha256.Sum256([]byte(pair[0]))
		if hex.EncodeToString(digest[:]) != pair[1] {
			t.Fatal("schema profile checksum")
		}
	}
	return f, files, contract.AuditCapacity{Events: f.Limits.Events, EnvelopeBytes: f.Limits.EnvelopeBytes, Deliveries: f.Limits.Deliveries}
}
func TestAuditCapacityUpgradeInput(t *testing.T) {
	_, f, limits := capacityUpgradeData(t)
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	ctx := context.Background()
	if e := PrepareAuditCapacityUpgrade(ctx, db, f[0], f[1], limits, false); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	for i := range 2 {
		altered := f
		altered[i] = append(append([]byte{}, f[i]...), ' ')
		if e := PrepareAuditCapacityUpgrade(ctx, db, altered[0], altered[1], limits, true); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	for _, bad := range []contract.AuditCapacity{{Events: 0, EnvelopeBytes: 1, Deliveries: 1}, {Events: 1, EnvelopeBytes: -1, Deliveries: 1}, {Events: 1, EnvelopeBytes: 1, Deliveries: maxSafe + 1}} {
		if e := PrepareAuditCapacityUpgrade(ctx, db, f[0], f[1], bad, true); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	if e := PrepareAuditCapacityUpgrade(nil, db, f[0], f[1], limits, true); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if e := PrepareAuditCapacityUpgrade(ctx, nil, f[0], f[1], limits, true); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if e := PrepareAuditCapacityUpgrade(ctx, db, f[0], f[1], limits, true); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestAuditCapacityUpgradeLive(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CAPACITY_UPGRADE_URL")
	if url == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	fixture, files, limits := capacityUpgradeData(t)
	// Start from the supported activated v2 upgrade, not a hand-written ready flag.
	base, e := os.ReadFile("../../../../contracts/persistence/session/0001.sql")
	if e != nil {
		t.Fatal(e)
	}
	if e = MigrateSession(ctx, db, base); e != nil {
		t.Fatal(e)
	}
	old := upgradeFiles(t)
	if e = PrepareAuditUpgrade(ctx, db, old[0], old[1], old[2], true); e != nil {
		t.Fatal(e)
	}
	step, e := AdvanceAuditUpgrade(ctx, db, 100, true, backfillPort(t, db, "ledger").validate)
	if e != nil || step.Status != "coverage_complete" {
		t.Fatal(step, e)
	}
	if e = ActivateAuditUpgrade(ctx, db, true, backfillPort(t, db, "ledger").validate); e != nil {
		t.Fatal(e)
	}
	// Immutable completed/recovered/pending history exists before capacity preparation.
	for _, product := range []string{"capacity-complete", "capacity-recovered", "capacity-pending"} {
		f := recoveryData(t, product)
		a := backfillPort(t, db, product)
		if _, e = a.AppendDecision(ctx, f.Request); e != nil {
			t.Fatal(e)
		}
		if product == "capacity-complete" {
			f.Terminal["outcome"] = "allow"
			if _, e = a.AppendDecision(ctx, f.Terminal); e != nil {
				t.Fatal(e)
			}
		}
		if product == "capacity-recovered" {
			if _, e = a.RecoverDecision(ctx, f.Terminal, f.Administration, f.Cutoff); e != nil {
				t.Fatal(e)
			}
		}
	}
	var history string
	if e = db.QueryRow("SELECT jsonb_agg(to_jsonb(e) ORDER BY product_id,event_id)::text FROM heimdall.audit_events e").Scan(&history); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditCapacityUpgrade(ctx, db, files[0], files[1], limits, true); e != nil {
		t.Fatal("prepare", e)
	}
	var after string
	if e = db.QueryRow("SELECT jsonb_agg(to_jsonb(e) ORDER BY product_id,event_id)::text FROM heimdall.audit_events e").Scan(&after); e != nil || history != after {
		t.Fatal("history changed", e)
	}
	if e = CheckAuditReadiness(ctx, db); e == nil {
		t.Fatal("v2 runtime still ready")
	}
	f := recoveryData(t, "new-writer")
	if _, e = backfillPort(t, db, "new-writer").AppendRuntime(ctx, f.Request); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e = backfillPort(t, db, "new-writer").AppendCapacity(ctx, f.Request); !errors.Is(e, contract.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	// Independent deployment processes can retry the exact preparation safely.
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			peer, e := sql.Open("postgres", url)
			if e == nil {
				defer peer.Close()
				e = PrepareAuditCapacityUpgrade(ctx, peer, files[0], files[1], limits, true)
			}
			results <- e
		}()
	}
	wg.Wait()
	for range 4 {
		if e = <-results; e != nil {
			t.Fatal("concurrent retry", e)
		}
	}
	changed := limits
	changed.Events++
	if e = PrepareAuditCapacityUpgrade(ctx, db, files[0], files[1], changed, true); !errors.Is(e, contract.ErrConflict) {
		t.Fatal(e)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			tx, e := db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			if _, e = tx.Exec(c.SQL); e != nil {
				t.Fatal("mutation", e)
			}
			if e = checkPreparedAuditCapacity(ctx, tx); e == nil {
				t.Fatal("accepted drift")
			}
		})
	}
	// Offline import can bind already-resolved decisions without transient future
	// reservations. This SQL proof is not semantic backfill orchestration.
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec("INSERT INTO heimdall_capacity.event_charges(product_id,environment,event_id) SELECT product_id,environment,event_id FROM heimdall.audit_events"); e != nil {
		t.Fatal(e)
	}
	recovered := recoveryData(t, "capacity-recovered")
	if _, e = tx.Exec(`INSERT INTO heimdall_capacity.reservations(product_id,environment,decision_id,request_event_id,resolution,terminal_event_id,recovery_event_id)
 SELECT product_id,environment,decision_id,request_event_id,resolution,terminal_event_id,CASE WHEN resolution='recovered' THEN $1::text ELSE NULL END FROM heimdall.audit_decisions`, recovered.Administration["eventId"]); e != nil {
		t.Fatal(e)
	}
	var used, reserved int
	if e = tx.QueryRow("SELECT used_events,reserved_events FROM heimdall_capacity.policy").Scan(&used, &reserved); e != nil || used != 6 || reserved != 2 {
		t.Fatal(used, reserved, e)
	}
	if _, e = tx.Exec("SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	tx.Rollback()
	// A ready policy must refuse resolved imports even when slot and charges match.
	tx, e = db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec("INSERT INTO heimdall_capacity.event_charges(product_id,environment,event_id) SELECT product_id,environment,event_id FROM heimdall.audit_events; UPDATE heimdall_capacity.policy SET state='ready'"); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec("INSERT INTO heimdall_capacity.reservations(product_id,environment,decision_id,request_event_id,resolution,terminal_event_id) SELECT product_id,environment,decision_id,request_event_id,resolution,terminal_event_id FROM heimdall.audit_decisions WHERE resolution='completed'"); e == nil {
		t.Fatal("resolved runtime insert accepted")
	}
	tx.Rollback()
	// Cancellation while waiting for the deployment lock leaves preparation intact.
	holder, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Close()
	lock, e := holder.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Rollback()
	if _, e = lock.Exec("SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		t.Fatal(e)
	}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	e = PrepareAuditCapacityUpgrade(short, db, files[0], files[1], limits, true)
	cancel()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal("lock cancellation", e)
	}
	lock.Rollback()
	if e = PrepareAuditCapacityUpgrade(ctx, db, files[0], files[1], limits, true); e != nil {
		t.Fatal("retry after cancellation", e)
	}
	// Simulate one committed deployment backfill page. Preparation retries must
	// preserve its charge and cursor rather than reinitialize either record.
	if _, e = db.Exec(`BEGIN;
 INSERT INTO heimdall_capacity.event_charges(product_id,environment,event_id) SELECT product_id,environment,event_id FROM heimdall.audit_events ORDER BY product_id COLLATE "C",environment COLLATE "C",event_id COLLATE "C" LIMIT 1;
 UPDATE heimdall_capacity.backfill SET (after_product,after_environment,after_id,scanned_events)=(SELECT product_id,environment,event_id,1 FROM heimdall_capacity.event_charges);
 COMMIT;`); e != nil {
		t.Fatal(e)
	}
	var progressBefore, progressAfter string
	if e = db.QueryRow("SELECT row_to_json(b)::text FROM heimdall_capacity.backfill b").Scan(&progressBefore); e != nil {
		t.Fatal(e)
	}
	if e = PrepareAuditCapacityUpgrade(ctx, db, files[0], files[1], limits, true); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow("SELECT row_to_json(b)::text FROM heimdall_capacity.backfill b").Scan(&progressAfter); e != nil || progressBefore != progressAfter {
		t.Fatal("reset checkpoint", e)
	}
	if e = db.QueryRow("SELECT used_events FROM heimdall_capacity.policy").Scan(&used); e != nil || used != 1 {
		t.Fatal("reset usage", used, e)
	}

}
