package heimdall_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/postgres"
	"os"
	"strconv"
	"testing"
)

func TestPublicCapacity(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_CAPACITY_READY_URL")
	if url == "" {
		t.Skip("activated capacity database required")
	}
	admin, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	db, e := sql.Open("postgres", url+"&options=-c%20role%3Dpublic_capacity_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var f struct{ Request, Normal, Ordinary map[string]any }
	raw, e := os.ReadFile("../../contracts/fixtures/audit-capacity-writes.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	for _, event := range []map[string]any{f.Request, f.Normal, f.Ordinary} {
		event["productId"] = "retained-public-go"
		event["environment"] = "production"
	}
	storage, e := postgres.NewStorage(db, postgres.StorageOptions{ProductID: "retained-public-go", Environment: "production", AuditSinks: []string{"siem"}})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if e = storage.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	var original int64
	if e = admin.QueryRow("SELECT event_limit FROM heimdall_capacity.policy").Scan(&original); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := admin.Exec("UPDATE heimdall.schema_metadata SET state='ready',database_version=4; UPDATE heimdall_capacity.policy SET event_limit=" + strconv.FormatInt(original, 10) + "; UPDATE heimdall_capacity.upgrade SET event_limit=" + strconv.FormatInt(original, 10)); e != nil {
			t.Fatal(e)
		}
	}()
	if _, e = admin.Exec("UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events+3; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)"); e != nil {
		t.Fatal(e)
	}
	receipt, e := storage.AppendAudit(ctx, f.Request)
	if e != nil {
		t.Fatal(e)
	}
	retry, e := storage.AppendAudit(ctx, f.Request)
	if e != nil || retry != receipt {
		t.Fatal(retry, e)
	}
	if _, e = storage.AppendAudit(ctx, f.Ordinary); !errors.Is(e, heimdall.ErrAuditUnavailable) {
		t.Fatal(e)
	}
	receipt, e = storage.AppendAudit(ctx, f.Normal)
	if e != nil {
		t.Fatal(e)
	}
	retry, e = storage.AppendAudit(ctx, f.Normal)
	if e != nil || retry != receipt {
		t.Fatal(retry, e)
	}
	for _, table := range []string{"heimdall.audit_events", "heimdall_capacity.event_charges"} {
		var n int
		if e = admin.QueryRow("SELECT count(*) FROM " + table + " WHERE product_id='retained-public-go'").Scan(&n); e != nil || n != 2 {
			t.Fatal(n, e)
		}
	}
	for _, state := range []string{"migrating", "ready"} {
		if _, e = admin.Exec("UPDATE heimdall.schema_metadata SET state=$1,database_version=2", state); e != nil {
			t.Fatal(e)
		}
		if e = storage.Ready(ctx); !errors.Is(e, heimdall.ErrUnavailable) {
			t.Fatal(e)
		}
		if _, e = storage.AppendAudit(ctx, f.Normal); !errors.Is(e, heimdall.ErrUnavailable) {
			t.Fatal(e)
		}
	}
}
