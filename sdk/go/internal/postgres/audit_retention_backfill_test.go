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
)

func TestRetentionBackfillInput(t *testing.T) {
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	ctx := context.Background()
	validate := func(map[string]any) error { return nil }
	raw, err := os.ReadFile("../../../../contracts/fixtures/audit-retention-backfill.json")
	var fixture struct{ InvalidBatchSizes []int }
	if err != nil || json.Unmarshal(raw, &fixture) != nil || len(fixture.InvalidBatchSizes) == 0 {
		t.Fatal("fixture", err)
	}
	for _, limit := range fixture.InvalidBatchSizes {
		if _, e := AdvanceAuditRetentionUpgrade(ctx, db, limit, true, validate); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	for _, run := range []func() error{
		func() error { _, e := AdvanceAuditRetentionUpgrade(nil, db, 1, true, validate); return e },
		func() error { _, e := AdvanceAuditRetentionUpgrade(ctx, nil, 1, true, validate); return e },
		func() error { _, e := AdvanceAuditRetentionUpgrade(ctx, db, 1, false, validate); return e },
		func() error { _, e := AdvanceAuditRetentionUpgrade(ctx, db, 1, true, nil); return e },
	} {
		if e := run(); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := AdvanceAuditRetentionUpgrade(ctx, db, 1, true, validate); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}

func TestRetentionBackfillBudgetSeed(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETENTION_BUDGET_SEED_URL")
	if url == "" {
		t.Skip("activated v3 seed database required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	a := backfillPort(t, db, "a-budget")
	event := capacityWritesData(t).Ordinary
	event["productId"] = "a-budget"
	event["target"] = map[string]any{"type": "document", "id": strings.Repeat("🚀", 8192)}
	for i := range 100 {
		event["eventId"] = fmt.Sprintf("budget-%03d", i)
		if _, e = a.AppendRuntime(context.Background(), event); e != nil {
			t.Fatal(e)
		}
	}
}
