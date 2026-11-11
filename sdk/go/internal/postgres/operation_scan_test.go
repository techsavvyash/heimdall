package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestOperationScanInput(t *testing.T) {
	f := journalData(t)
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	s := operationStore(t, db, f, "scan", nil)
	bad := "bad/id"
	for _, input := range []struct {
		queue string
		after *string
		now   int64
		limit int
	}{{"runnable", nil, -1, 1}, {"runnable", &bad, 0, 1}, {"runnable", nil, 0, 101}, {"unknown", nil, 0, 1}} {
		if _, e := s.Scan(context.Background(), input.queue, input.after, input.now, input.limit); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Scan(ctx, "recovery", nil, 0, 1); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestOperationScan(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if dsn == "" {
		t.Skip("managed database")
	}
	db, _ := sql.Open("postgres", dsn)
	defer db.Close()
	f := journalData(t)
	s := operationStore(t, db, f, "transition-ts", nil)
	ctx := context.Background()
	var vectors struct {
		Cases []struct {
			Queue string
			Now   int64
			IDs   []string
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/operation-scan.json")
	if e != nil || json.Unmarshal(raw, &vectors) != nil {
		t.Fatal("fixture", e)
	}
	snapshot := func() string {
		var result string
		e := db.QueryRow("SELECT jsonb_agg(jsonb_build_array(operation_id,state,generation,dispatch_count) ORDER BY operation_id)::text FROM heimdall_managed.operations WHERE product_id='transition-ts'").Scan(&result)
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	budget := operationStore(t, db, f, "scan-budget", nil)
	firstBudget, err := budget.Scan(ctx, "runnable", nil, 100, 100)
	if err != nil || len(firstBudget.Items) != 3 || firstBudget.NextOperationID == nil || *firstBudget.NextOperationID != "large-2" {
		t.Fatal(firstBudget, err)
	}
	lastBudget, err := budget.Scan(ctx, "runnable", firstBudget.NextOperationID, 100, 100)
	if err != nil || len(lastBudget.Items) != 2 || lastBudget.Items[0].OperationID != "large-3" || lastBudget.Items[1].OperationID != "large-4" || lastBudget.NextOperationID != nil {
		t.Fatal(lastBudget, err)
	}
	before := snapshot()
	for _, c := range vectors.Cases {
		all, e := s.Scan(ctx, c.Queue, nil, c.Now, 100)
		if e != nil {
			t.Fatal(e)
		}
		ids := []string{}
		for _, r := range all.Items {
			ids = append(ids, r.OperationID)
		}
		if !reflect.DeepEqual(ids, c.IDs) || all.NextOperationID != nil {
			t.Fatal(ids, c.IDs)
		}
		ids = []string{}
		var after *string
		for {
			page, err := s.Scan(ctx, c.Queue, after, c.Now, 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range page.Items {
				ids = append(ids, r.OperationID)
			}
			after = page.NextOperationID
			if after == nil {
				break
			}
		}
		if !reflect.DeepEqual(ids, c.IDs) {
			t.Fatal(ids, c.IDs)
		}
	}
	other := operationStore(t, db, f, "other", nil)
	page, e := other.Scan(ctx, "recovery", nil, 300, 100)
	if e != nil || len(page.Items) != 0 {
		t.Fatal(page, e)
	}
	binding := f.Binding
	binding.ProductID = "transition-ts"
	binding.Environment = "staging"
	env, _ := NewOperationRecords(db, binding, nil)
	page, e = env.Scan(ctx, "recovery", nil, 300, 100)
	if e != nil || len(page.Items) != 0 {
		t.Fatal(page, e)
	}
	binding.Environment = f.Binding.Environment
	binding.ModelID = "wrong"
	wrong, _ := NewOperationRecords(db, binding, nil)
	if _, e = wrong.Scan(ctx, "recovery", nil, 300, 100); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	if snapshot() != before {
		t.Fatal("scan mutated journal")
	}
	ro, _ := sql.Open("postgres", dsn+"&options=-c%20default_transaction_read_only%3Don")
	defer ro.Close()
	readonly := operationStore(t, ro, f, "missing", nil)
	if _, e = readonly.Scan(ctx, "recovery", nil, 0, 1); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	lock, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock.Exec("LOCK TABLE heimdall_managed.operations IN ACCESS EXCLUSIVE MODE"); e != nil {
		lock.Rollback()
		t.Fatal(e)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = s.Scan(deadline, "recovery", nil, 0, 1)
	cancel()
	lock.Rollback()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	page, e = s.Scan(ctx, "recovery", nil, 0, 100)
	if e != nil || len(page.Items) != 2 {
		t.Fatal(page, e)
	}
}
