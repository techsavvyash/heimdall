package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"testing"
)

func TestRetentionActivationInput(t *testing.T) {
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	ctx := context.Background()
	validate := func(map[string]any) error { return nil }
	for _, run := range []func() error{
		func() error { return ActivateAuditRetentionUpgrade(nil, db, true, validate) },
		func() error { return ActivateAuditRetentionUpgrade(ctx, nil, true, validate) },
		func() error { return ActivateAuditRetentionUpgrade(ctx, db, false, validate) },
		func() error { return ActivateAuditRetentionUpgrade(ctx, db, true, nil) },
	} {
		if e := run(); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if e := ActivateAuditRetentionUpgrade(ctx, db, true, validate); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
