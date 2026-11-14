package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestSetupJournal(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_SETUP_DSN")
	if dsn == "" {
		t.Skip("requires setup journal database")
	}
	connector, err := NewConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	scope := SetupScope{"support", "setup-go", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2}
	const first = "da11dd4d-e7a5-4e88-969c-6e92861f5781"
	const second = "da11dd4d-e7a5-4e88-969c-6e92861f5782"
	step, err := ClaimSetupStep(ctx, db, scope, 1, first)
	if err != nil || step != (SetupStep{State: "claimed", DispatchID: first}) {
		t.Fatal("initial claim", err)
	}
	step, err = ClaimSetupStep(ctx, db, scope, 1, second)
	if err != nil || step != (SetupStep{State: "unresolved", DispatchID: first}) {
		t.Fatal("restart", err)
	}
	if _, err = ClaimSetupStep(ctx, db, scope, 2, second); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("step order", err)
	}
	for range 2 {
		if err = ConfirmSetupStep(ctx, db, scope, 1, first, "project_1"); err != nil {
			t.Fatal(err)
		}
	}
	if err = ConfirmSetupStep(ctx, db, scope, 1, first, "project_2"); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("overwrote confirmation", err)
	}
	step, err = ClaimSetupStep(ctx, db, scope, 1, second)
	if err != nil || step != (SetupStep{"confirmed", first, "project_1"}) {
		t.Fatal("confirmed claim", err)
	}
	step, err = ClaimSetupStep(ctx, db, scope, 2, second)
	if err != nil || step.State != "claimed" {
		t.Fatal("next claim", err)
	}
}
