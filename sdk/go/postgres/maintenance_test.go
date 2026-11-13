package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/lib/pq"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
)

func TestRecordMaintenancePublicPort(t *testing.T) {
	db, err := sql.Open("postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = NewRecordMaintenance(db, "../bad", "test"); !errors.Is(err, heimdall.ErrInvalidArgument) {
		t.Fatal(err)
	}
	maintenance, err := NewRecordMaintenance(db, "cleanup", "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := maintenance.CleanupExpired(ctx, 1); !errors.Is(err, heimdall.ErrCancelled) {
		t.Fatal(err)
	}
}
