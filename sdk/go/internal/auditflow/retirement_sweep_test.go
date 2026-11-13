package auditflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestRetirementSweep(t *testing.T) {
	var f struct {
		Sweep      RetirementSweep
		Events     []string
		Operations []MaintenanceOperation
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/retirement-sweep.json")
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("invalid sweep fixture", err)
	}
	command := &MaintenanceCommand{auth: &Authentication{policy: "p1", writer: &Decision{
		product: "ledger", environment: "production", now: func() time.Time { return time.UnixMilli(f.Sweep.At) },
	}}, modelID: "pinned-model", manifestVersion: 7, retainForMS: 1000}
	ctx := context.Background()
	page := &contract.RetirementPage{EventIDs: f.Events}
	scan := func(_ context.Context, tenant *string, at, duration int64, after *contract.RetirementCursor, limit int) (*contract.RetirementPage, error) {
		if tenant != nil || at != f.Sweep.At || duration != 1000 || after != nil || limit != 8 {
			t.Fatal("scan lost sweep scope")
		}
		return page, nil
	}
	var stored RetirementBatch
	save := func(_ context.Context, b RetirementBatch) (bool, error) {
		if !reflect.DeepEqual(b.Operations, f.Operations) {
			t.Fatal("wrong proposals", b.Operations)
		}
		if stored.Operations != nil {
			return false, nil
		}
		stored = b
		return false, contract.ErrOutcomeUnknown
	}
	if err := command.QueueRetirementPage(ctx, f.Sweep, 8, scan, save); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if err := command.QueueRetirementPage(ctx, f.Sweep, 8, scan, save); !errors.Is(err, contract.ErrConflict) {
		t.Fatal(err)
	}
	stored.PolicyVersion = "p2"
	if _, err := command.RunQueuedRetirement(ctx, "unused", stored, 0); !errors.Is(err, contract.ErrConflict) {
		t.Fatal(err)
	}
	page.EventIDs = nil
	page.Next = &contract.RetirementCursor{ProductID: "ledger", Environment: "production", Now: f.Sweep.At, RetainForMS: 1000, EventID: "cursor"}
	if err := command.QueueRetirementPage(ctx, f.Sweep, 8, scan, func(_ context.Context, b RetirementBatch) (bool, error) {
		return len(b.Operations) == 0 && b.Next.EventID == "cursor", nil
	}); err != nil {
		t.Fatal(err)
	}
}
