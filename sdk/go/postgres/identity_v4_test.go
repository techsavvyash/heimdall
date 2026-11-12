package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"os"
	"testing"
	"time"
)

func TestFactoryIdentityV4(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_FACTORY_URL")
	if url == "" {
		t.Skip("activated v4 database required")
	}
	var f struct {
		Identity  heimdall.Identity
		Negatives []heimdall.IdentityNegative
		Legacy    []string
	}
	raw, e := os.ReadFile("../../../contracts/fixtures/public-runtime-v4.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	db, port := v4Storage(t, url, "identity-go")
	defer db.Close()
	admin, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_FACTORY_ADMIN"))
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	ctx := context.Background()
	assertRefused := func(p heimdall.RuntimeStorage) {
		t.Helper()
		if _, e := p.Identity.Check(ctx, f.Identity); !errors.Is(e, heimdall.ErrUnavailable) {
			t.Fatal("identity read", e)
		}
		for _, n := range f.Negatives {
			if e := p.Identity.Invalidate(ctx, n); !errors.Is(e, heimdall.ErrUnavailable) {
				t.Fatal("identity write", n.Reason, e)
			}
		}
	}
	for _, key := range f.Legacy {
		other, p := v4Storage(t, os.Getenv(key), "identity-go")
		assertRefused(p)
		other.Close()
	}
	readonly, p := v4Storage(t, url+"%20-c%20default_transaction_read_only%3Don", "identity-go")
	assertRefused(p)
	readonly.Close()
	if _, e = admin.Exec("UPDATE heimdall.schema_metadata SET state='migrating'"); e != nil {
		t.Fatal(e)
	}
	assertRefused(port)
	if _, e = admin.Exec("UPDATE heimdall.schema_metadata SET state='ready'"); e != nil {
		t.Fatal(e)
	}
	tx, e := admin.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec("SELECT pg_advisory_xact_lock(74162401)"); e != nil {
		t.Fatal(e)
	}
	for _, n := range f.Negatives {
		waiting, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		e = port.Identity.Invalidate(waiting, n)
		cancel()
		if !errors.Is(e, heimdall.ErrDeadlineExceeded) {
			tx.Rollback()
			t.Fatal(e)
		}
	}
	tx.Rollback()
	var count int
	if e = admin.QueryRow("SELECT count(*) FROM heimdall.identity_denials WHERE environment='identity-go'").Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if denied, e := port.Identity.Check(ctx, f.Identity); e != nil || denied {
		t.Fatal(denied, e)
	}
	for _, n := range f.Negatives {
		if e = port.Identity.Invalidate(ctx, n); e != nil {
			t.Fatal(e)
		}
	}
	if denied, e := port.Identity.Check(ctx, f.Identity); e != nil || !denied {
		t.Fatal(denied, e)
	}
	other, isolated := v4Storage(t, url, "identity-go-other")
	defer other.Close()
	if denied, e := isolated.Identity.Check(ctx, f.Identity); e != nil || denied {
		t.Fatal(denied, e)
	}
}
