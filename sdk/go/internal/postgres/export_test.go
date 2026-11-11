package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"strings"
	"testing"
	"time"
)

type exportFixture struct {
	Cases []struct {
		Name, Kind string
		Row        map[string]any
		Valid      bool
	}
	Seed                           []string
	WorkspaceCount, OperationCount int
}

func readExportFixture(t *testing.T) exportFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/managed-export.json")
	if e != nil {
		t.Fatal(e)
	}
	var f exportFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	return f
}
func TestExportDecoder(t *testing.T) {
	f := readExportFixture(t)
	workspace := "workspace"
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			item, e := decodeExportRow(c.Row, c.Kind, "export", "test", &workspace)
			if c.Valid {
				if e != nil || item["createdAt"] != int64(1) || item["updatedAt"] != int64(2) || len(item) != len(c.Row) {
					t.Fatal(item, e)
				}
			} else if !errors.Is(e, contract.ErrUnavailable) {
				t.Fatal(e)
			}
		})
	}
}
func TestExportInput(t *testing.T) {
	db, e := sql.Open("postgres", "")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, _ := NewManagedExport(db, "export", "test")
	ctx := context.Background()
	for _, limit := range []int{0, 101} {
		if _, e = s.Scan(ctx, "workspaces", nil, nil, limit); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	if _, e = s.Scan(ctx, "operations", nil, nil, 1); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	bad := "../bad"
	if _, e = s.Scan(ctx, "workspaces", &bad, nil, 1); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	bad = ""
	if _, e = s.Scan(ctx, "workspaces", nil, &bad, 1); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.Scan(cancelled, "workspaces", nil, nil, 1); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}
func TestExportPrimary(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_MANAGED_DATABASE_URL")
	if url == "" {
		t.Skip("live PostgreSQL")
	}
	f := readExportFixture(t)
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	restricted, e := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_export_reader")
	if e != nil {
		t.Fatal(e)
	}
	defer restricted.Close()
	for _, q := range f.Seed {
		if _, e = db.Exec(strings.ReplaceAll(q, "$scope", "export-go")); e != nil {
			t.Fatal(e)
		}
	}
	ctx := context.Background()
	type page struct {
		Items  []map[string]any
		NextID *string
	}
	read := func(s *ManagedExport, kind string, w, after *string, limit int) page {
		t.Helper()
		raw, e := s.Scan(ctx, kind, w, after, limit)
		if e != nil {
			t.Fatal(e)
		}
		for _, bad := range []string{"PRIVATE", `"name"`, `"actorHash"`, `"targetHash"`, `"keyDigest"`, `"fingerprint"`, `"intent"`, `"envelope"`} {
			if strings.Contains(string(raw), bad) {
				t.Fatal("sensitive export field")
			}
		}
		var p page
		if json.Unmarshal(raw, &p) != nil {
			t.Fatal("page")
		}
		return p
	}
	for _, scope := range []string{"export-go", "export-ts"} {
		s, _ := NewManagedExport(restricted, scope, "test")
		for _, kind := range []string{"workspaces", "operations"} {
			var w *string
			total := f.WorkspaceCount
			expected := "w-099"
			if kind == "operations" {
				id := "w-000"
				w = &id
				total = f.OperationCount
				expected = "op-099"
			}
			first := read(s, kind, w, nil, 100)
			if len(first.Items) != 100 || first.NextID == nil || *first.NextID != expected {
				t.Fatal("first page", first.NextID)
			}
			last := read(s, kind, w, first.NextID, 100)
			if len(last.Items) != total-100 || last.NextID != nil {
				t.Fatal("last page")
			}
			if kind == "operations" && last.Items[len(last.Items)-1]["state"] != "uncertain" {
				t.Fatal("quarantine omitted")
			}
		}
		w := "w-002"
		if len(read(s, "workspaces", &w, nil, 100).Items) != 1 {
			t.Fatal("workspace filter")
		}
		w = "w-001"
		if len(read(s, "operations", &w, nil, 100).Items) != 0 {
			t.Fatal("operation filter")
		}
	}
	absent, _ := NewManagedExport(restricted, "absent-export", "test")
	if len(read(absent, "workspaces", nil, nil, 100).Items) != 0 {
		t.Fatal("scope")
	}
	for _, q := range []string{"SELECT name FROM heimdall_managed.workspaces LIMIT 1", "SELECT intent_payload FROM heimdall_managed.operations LIMIT 1"} {
		if _, e = restricted.Exec(q); e == nil {
			t.Fatal("sensitive column privilege")
		}
	}
	ro, e := sql.Open("postgres", url+"&options=-c%20default_transaction_read_only%3Don")
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	s, _ := NewManagedExport(ro, "export-go", "test")
	if _, e = s.Scan(ctx, "workspaces", nil, nil, 1); !errors.Is(e, contract.ErrUnavailable) {
		t.Fatal(e)
	}
	lock, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Rollback()
	if _, e = lock.Exec("LOCK TABLE heimdall_managed.workspaces IN ACCESS EXCLUSIVE MODE"); e != nil {
		t.Fatal(e)
	}
	s, _ = NewManagedExport(restricted, "export-go", "test")
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, e = s.Scan(deadline, "workspaces", nil, nil, 1)
	cancel()
	if !errors.Is(e, contract.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	if e = lock.Rollback(); e != nil {
		t.Fatal(e)
	}
	if len(read(s, "workspaces", nil, nil, 1).Items) != 1 {
		t.Fatal("pool recovery")
	}
}
