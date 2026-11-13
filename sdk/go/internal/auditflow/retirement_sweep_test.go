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
		Sweep        RetirementSweep
		Events       []string
		Operations   []MaintenanceOperation
		Continuation RetirementContinuation
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
	page.Next = &f.Continuation.RetirementCursor
	if err := command.QueueRetirementPage(ctx, f.Sweep, 8, scan, func(_ context.Context, b RetirementBatch) (bool, error) {
		return len(b.Operations) == 0 && reflect.DeepEqual(b.Next, &f.Continuation), nil
	}); err != nil {
		t.Fatal(err)
	}
	resumed := f.Sweep
	resumed.After = &f.Continuation
	rescan := func(_ context.Context, _ *string, _, _ int64, after *contract.RetirementCursor, _ int) (*contract.RetirementPage, error) {
		if !reflect.DeepEqual(after, &f.Continuation.RetirementCursor) {
			t.Fatal("wrong resume position", after)
		}
		return &contract.RetirementPage{}, nil
	}
	finish := func(_ context.Context, b RetirementBatch) (bool, error) { return b.Next == nil, nil }
	if err := command.QueueRetirementPage(ctx, resumed, 8, rescan, finish); err != nil {
		t.Fatal(err)
	}
	command.modelID = "changed-model"
	if err := command.QueueRetirementPage(ctx, resumed, 8, scan, save); !errors.Is(err, contract.ErrConflict) {
		t.Fatal(err)
	}
	command.modelID = "pinned-model"
	resumed.ID = "different-sweep"
	if err := command.QueueRetirementPage(ctx, resumed, 8, scan, save); !errors.Is(err, contract.ErrConflict) {
		t.Fatal(err)
	}
}
