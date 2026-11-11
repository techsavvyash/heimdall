package auditflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"reflect"
	"testing"
	"time"
)

func fixtures(t *testing.T) (map[string]any, func(map[string]any) error) {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-digests-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var f []struct{ Event map[string]any }
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	raw, e = os.ReadFile("../../../../contracts/audit-event.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var schema any
	if e = json.Unmarshal(raw, &schema); e != nil {
		t.Fatal(e)
	}
	compiler := jsonschema.NewCompiler()
	if e = compiler.AddResource("https://test/audit", schema); e != nil {
		t.Fatal(e)
	}
	shape, e := compiler.Compile("https://test/audit")
	if e != nil {
		t.Fatal(e)
	}
	return f[0].Event, func(event map[string]any) error {
		if shape.Validate(event) != nil {
			return contract.ErrInvalidArgument
		}
		return nil
	}
}
func receipt(event map[string]any) contract.AuditReceipt {
	digest, _ := contract.AuditDigest(event)
	return contract.AuditReceipt{ProductID: event["productId"].(string), Environment: event["environment"].(string), EventID: event["eventId"].(string), RecordedAt: int64(event["recordedAt"].(float64)), PayloadDigest: digest}
}
func TestDecisions(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/audited-decision.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name           string
		Value          bool
		Outcome, Error string
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			input, validate := fixtures(t)
			order := []string{}
			events := []map[string]any{}
			runner, e := New("ledger", "production", func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
				order = append(order, event["outcome"].(string))
				events = append(events, event)
				return receipt(event), nil
			}, validate, nil)
			if e != nil {
				t.Fatal(e)
			}
			value, e := runner.Run(context.Background(), input, func(context.Context) (bool, error) {
				order = append(order, "evaluate")
				if c.Name == "arbitrary error" {
					return false, errors.New("sensitive token")
				}
				if c.Error != "" {
					return false, contract.NewError(contract.Code(c.Error), "discarded-operation-id")
				}
				return c.Value, nil
			})
			if c.Error != "" {
				if e == nil || e.Error() != c.Error {
					t.Fatalf("wrong error %v", e)
				}
			} else if e != nil || value != c.Value {
				t.Fatalf("wrong value %v %v", value, e)
			}
			if !reflect.DeepEqual(order, []string{"requested", "evaluate", c.Outcome}) {
				t.Fatal(order)
			}
			if events[0]["decisionId"] != events[1]["decisionId"] || events[0]["eventId"] == events[1]["eventId"] || events[0]["decisionId"] == input["decisionId"] {
				t.Fatal("correlation mismatch")
			}
		})
	}
}
func TestFailureBoundaries(t *testing.T) {
	for _, at := range []int{1, 2} {
		input, validate := fixtures(t)
		calls := 0
		evaluated := false
		runner, _ := New("ledger", "production", func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
			calls++
			if calls == at {
				return contract.AuditReceipt{}, errors.New("secret")
			}
			return receipt(event), nil
		}, validate, nil)
		_, e := runner.Run(context.Background(), input, func(context.Context) (bool, error) { evaluated = true; return true, nil })
		if e != contract.ErrAuditUnavailable || evaluated != (at == 2) {
			t.Fatalf("failure boundary %d: %v", at, e)
		}
	}
	for _, field := range []string{"product", "environment", "event", "digest", "time"} {
		input, validate := fixtures(t)
		evaluated := false
		runner, _ := New("ledger", "production", func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
			r := receipt(event)
			switch field {
			case "product":
				r.ProductID = "other"
			case "environment":
				r.Environment = "other"
			case "event":
				r.EventID = "other"
			case "digest":
				r.PayloadDigest = "other"
			case "time":
				r.RecordedAt = 0
			}
			return r, nil
		}, validate, nil)
		_, e := runner.Run(context.Background(), input, func(context.Context) (bool, error) { evaluated = true; return true, nil })
		if e != contract.ErrAuditUnavailable || evaluated {
			t.Fatal("accepted bad receipt", field)
		}
	}
}
func TestCancellation(t *testing.T) {
	input, validate := fixtures(t)
	outcomes := []string{}
	evaluated := false
	runner, _ := New("ledger", "production", func(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
		if ctx.Err() != nil {
			t.Error("audit context cancelled")
		}
		outcomes = append(outcomes, event["outcome"].(string))
		return receipt(event), nil
	}, validate, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := runner.Run(ctx, input, func(context.Context) (bool, error) { evaluated = true; return true, nil })
	if !errors.Is(e, contract.ErrCancelled) || evaluated || !reflect.DeepEqual(outcomes, []string{"requested", "cancelled"}) {
		t.Fatal(e, outcomes)
	}
	outcomes = nil
	release := make(chan struct{})
	defer close(release)
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, e = runner.Run(ctx, input, func(context.Context) (bool, error) { <-release; return true, nil })
	if !errors.Is(e, contract.ErrDeadlineExceeded) || !reflect.DeepEqual(outcomes, []string{"requested", "unavailable"}) {
		t.Fatal(e, outcomes)
	}
}
func TestScope(t *testing.T) {
	input, validate := fixtures(t)
	calls := 0
	runner, _ := New("ledger", "production", func(context.Context, map[string]any) (contract.AuditReceipt, error) {
		calls++
		return contract.AuditReceipt{}, nil
	}, validate, nil)
	input["productId"] = "support"
	if _, e := runner.Run(context.Background(), input, func(context.Context) (bool, error) { return true, nil }); e != contract.ErrInvalidArgument || calls != 0 {
		t.Fatal("cross-product accepted")
	}
}

func TestTerminalReceiptGate(t *testing.T) {
	input, validate := fixtures(t)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	runner, _ := New("ledger", "production", func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
		if event["outcome"] == "allow" {
			close(started)
			<-release
		}
		return receipt(event), nil
	}, validate, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, e := runner.Run(ctx, input, func(context.Context) (bool, error) { return true, nil })
		finished <- e
	}()
	<-started
	select {
	case e := <-finished:
		t.Fatal("released before terminal commit", e)
	default:
	}
	cancel()
	close(release)
	if e := <-finished; !errors.Is(e, contract.ErrCancelled) {
		t.Fatal("released after cancellation", e)
	}
}
