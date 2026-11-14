package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
)

func TestCapacityActivationInput(t *testing.T) {
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	validate := backfillPort(t, db, "ledger").validate
	ctx := context.Background()
	for _, args := range []struct {
		ctx      context.Context
		db       *sql.DB
		drained  bool
		validate func(map[string]any) error
	}{
		{nil, db, true, validate}, {ctx, nil, true, validate}, {ctx, db, false, validate}, {ctx, db, true, nil},
	} {
		if e := ActivateAuditCapacityUpgrade(args.ctx, args.db, args.drained, args.validate); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if e := ActivateAuditCapacityUpgrade(ctx, db, true, validate); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}

func TestCapacityActivationLive(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CAPACITY_ACTIVATE_URL")
	if url == "" {
		t.Skip("accounted PostgreSQL required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	validate := backfillPort(t, db, "ledger").validate
	mode := os.Getenv("HEIMDALL_TEST_CAPACITY_ACTIVATE_MODE")
	if mode == "validator" {
		validate = func(map[string]any) error { return contract.ErrAuditUnavailable }
	}
	e = ActivateAuditCapacityUpgrade(ctx, db, true, validate)
	if mode == "unknown" {
		if !errors.Is(e, contract.ErrOutcomeUnknown) {
			t.Fatal(e)
		}
		return
	}
	if mode == "reject" || mode == "validator" {
		if e == nil {
			t.Fatal("unsafe activation accepted")
		}
		var state string
		if e = db.QueryRow("SELECT state FROM heimdall.schema_metadata").Scan(&state); e != nil || state != "migrating" {
			t.Fatal(state, e)
		}
		var n int
		if e = db.QueryRow("SELECT count(*) FROM heimdall.migrations WHERE version=3").Scan(&n); e != nil || n != 0 {
			t.Fatal(n, e)
		}
		if e = db.QueryRow("SELECT state FROM heimdall_capacity.policy").Scan(&state); e != nil || state != "preparing" {
			t.Fatal(state, e)
		}
		return
	}
	if e != nil {
		t.Fatal(e)
	}
	var ready bool
	if e = db.QueryRow(capacityReadinessSQL, sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1]).Scan(&ready); e != nil || !ready {
		t.Fatal(ready, e)
	}
	if e = CheckAuditReadiness(ctx, db); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal("old protocol admitted", e)
	}
	// Legitimate post-activation history must not invalidate an activation retry.
	f := capacityWritesData(t)
	f.Ordinary["eventId"] = "after-activation"
	if _, e = backfillPort(t, db, "ledger").AppendCapacity(ctx, f.Ordinary); e != nil {
		t.Fatal(e)
	}
	if e = ActivateAuditCapacityUpgrade(ctx, db, true, validate); e != nil {
		t.Fatal(e)
	}
}
