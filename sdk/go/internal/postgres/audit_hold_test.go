package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRetainedHolds(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_HOLD_URL")
	if url == "" {
		t.Skip("v4 database required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	admin, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_HOLD_ADMIN"))
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	var f struct {
		Snapshot string
		Profiles map[string]struct {
			Target map[string]any
			Group  struct {
				Events        []map[string]any
				Hold, Corrupt struct {
					Request        contract.AuditHoldRequest
					Administration map[string]any
				}
			}
			Loss map[string]struct {
				Request        contract.AuditHoldRequest
				Administration map[string]any
			}
			Cases []struct {
				Name           string
				Request        contract.AuditHoldRequest
				Administration map[string]any
				Expected       string
				Stable         bool
			}
		}
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-hold-storage.json")
	if e != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal(e)
	}
	peer := os.Getenv("HEIMDALL_TEST_HOLD_PEER") == "1"
	language := "go"
	if peer {
		language = "ts"
	}
	if profile := os.Getenv("HEIMDALL_TEST_HOLD_PROFILE"); profile != "" {
		language = profile
	}
	p := f.Profiles[language]
	legacy := backfillPort(t, db, p.Target["productId"].(string))
	port, e := NewRetainedRuntimeAudit(db, legacy.product, "production", []string{"siem"}, legacy.validate)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	snapshot := func() string {
		var s string
		if e := admin.QueryRow(f.Snapshot).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	if os.Getenv("HEIMDALL_TEST_HOLD_REJECT") == "1" {
		before := snapshot()
		if _, e = port.ApplyHold(ctx, p.Cases[0].Request, p.Cases[0].Administration); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal(e)
		}
		if snapshot() != before {
			t.Fatal("readiness rejection changed snapshot")
		}
		return
	}
	if os.Getenv("HEIMDALL_TEST_HOLD_GROUP") == "1" {
		for _, event := range p.Group.Events {
			if _, e = port.Append(ctx, event); e != nil {
				t.Fatal(e)
			}
		}
		if _, e = port.ApplyHold(ctx, p.Group.Hold.Request, p.Group.Hold.Administration); e != nil {
			t.Fatal(e)
		}
		tenant := "tenant-1"
		if reason, err := port.Eligibility(ctx, &tenant, "z-request", 9000000000, 1); err != nil || reason != "retain_held" {
			t.Fatal(reason, err)
		}
		var saved string
		if e = admin.QueryRow("SELECT encode(payload_digest,'hex') FROM heimdall.audit_events WHERE product_id=$1 AND event_id='z-request'", legacy.product).Scan(&saved); e != nil {
			t.Fatal(e)
		}
		if _, e = admin.Exec("UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE product_id=$1 AND event_id='z-request'", legacy.product); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if _, err := admin.Exec("UPDATE heimdall.audit_events SET payload_digest=decode($2,'hex') WHERE product_id=$1 AND event_id='z-request'", legacy.product, saved); err != nil {
				t.Error(err)
			}
		}()
		before := snapshot()
		if _, e = port.ApplyHold(ctx, p.Group.Corrupt.Request, p.Group.Corrupt.Administration); !errors.Is(e, contract.ErrAuditUnavailable) {
			t.Fatal(e)
		}
		if snapshot() != before {
			t.Fatal("corrupt group changed snapshot")
		}
		return
	}
	if operation := os.Getenv("HEIMDALL_TEST_HOLD_OPERATION"); operation != "" {
		c := p.Loss[operation]
		before := snapshot()
		receipt, err := port.ApplyHold(ctx, c.Request, c.Administration)
		if os.Getenv("HEIMDALL_TEST_HOLD_FAULT") == "1" {
			if !errors.Is(err, contract.ErrOutcomeUnknown) {
				t.Fatal(err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		saved, err := port.ResolveReceipt(ctx, c.Administration)
		if err != nil || saved == nil || !reflect.DeepEqual(*saved, receipt) {
			t.Fatal(err)
		}
		if os.Getenv("HEIMDALL_TEST_HOLD_RETRY") == "1" && snapshot() != before {
			t.Fatal("retry changed snapshot")
		}
		var at sql.NullInt64
		if err = admin.QueryRow("SELECT released_at FROM heimdall_retention.event_holds WHERE product_id=$1 AND hold_id=$2", c.Request.ProductID, c.Request.HoldID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		if (operation == "release" && (!at.Valid || at.Int64 != 5001)) || (operation != "release" && at.Valid) {
			t.Fatal("hold state")
		}
		return
	}
	if !peer {
		if _, e = port.Append(ctx, p.Target); e != nil {
			t.Fatal(e)
		}
	}
	for _, c := range p.Cases {
		if peer && !strings.HasPrefix(c.Name, "exact") {
			continue
		}
		before := snapshot()
		receipt, err := port.ApplyHold(ctx, c.Request, c.Administration)
		status := "receipt"
		if err != nil {
			status = err.Error()
		}
		if status != c.Expected {
			t.Fatalf("%s: %s", c.Name, status)
		}
		if err == nil {
			saved, err := port.ResolveReceipt(ctx, c.Administration)
			if err != nil || saved == nil || !reflect.DeepEqual(*saved, receipt) {
				t.Fatal(c.Name, err)
			}
		}
		if (c.Stable || peer) && snapshot() != before {
			t.Fatal(c.Name, "changed snapshot")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = port.ApplyHold(cancelled, p.Cases[0].Request, p.Cases[0].Administration); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	var holds string
	if e = admin.QueryRow("SELECT string_agg(hold_id||':'||coalesce(released_at::text,'active'),',' ORDER BY hold_id) FROM heimdall_retention.event_holds WHERE product_id=$1", legacy.product).Scan(&holds); e != nil || holds != "hold-1:3001,retry-1:active" {
		t.Fatal(holds, e)
	}
}
