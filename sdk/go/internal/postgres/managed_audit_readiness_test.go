package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
)

func TestManagedAuditReadiness(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_FACTORY_ADMIN")
	if dsn == "" {
		t.Skip("live PostgreSQL required")
	}
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	if e = CheckManagedAuditReadiness(ctx, db); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/managed-audit-readiness.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name  string
		SQL   []string
		Ready bool
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixture")
	}
	for _, c := range cases {
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
			if e = tx.QueryRow(managedAuditReadinessSQL, sessionChecksum, decisionChecksum, managedChecksum).Scan(&ready); e != nil || ready != c.Ready {
				t.Fatal(ready, e)
			}
		})
	}
}
