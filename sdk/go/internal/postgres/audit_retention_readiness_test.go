package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
)

func TestRetentionReadiness(t *testing.T) {
	db, _ := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_RETENTION_READY_URL"))
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, check := range []func(context.Context, *sql.DB) error{CheckRetentionReadiness, CheckManagedRetentionReadiness} {
		if e := check(ctx, db); !errors.Is(e, contract.ErrCancelled) {
			t.Fatal(e)
		}
	}
	if os.Getenv("HEIMDALL_TEST_RETENTION_READY_URL") == "" {
		t.Skip("activated capacity database required")
	}
	ctx = context.Background()
	if e := CheckRetentionReadiness(ctx, db); e != nil {
		t.Fatal(e)
	}
	if e := CheckManagedRetentionReadiness(ctx, db); e != nil {
		t.Fatal(e)
	}
	if e := CheckCapacityReadiness(ctx, db); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name         string
			SQL          []string
			Ready        bool
			ManagedReady bool
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-retention-readiness.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			tx, e := db.Begin()
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			for _, q := range c.SQL {
				if _, e = tx.Exec(q); e != nil {
					t.Fatal(e)
				}
			}
			var ready bool
			e = tx.QueryRow(retentionReadinessSQL, retentionReadinessArgs()...).Scan(&ready)
			if e != nil || ready != c.Ready {
				t.Fatal(ready, e)
			}
			e = tx.QueryRow(managedRetentionReadinessSQL, append(retentionReadinessArgs(), managedChecksum)...).Scan(&ready)
			if e != nil || ready != c.ManagedReady {
				t.Fatal(ready, e)
			}
		})
	}
}
