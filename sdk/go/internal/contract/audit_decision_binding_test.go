package contract

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestAuditDecisionBinding(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-decision-binding.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		ProductID, Environment string
		Base                   map[string]map[string]any
		Cases                  []struct {
			Name   string
			Events []struct {
				Base  string
				Patch map[string]any
				Omit  []string
			}
			Valid    bool
			Expected map[string]any
		}
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	for _, c := range f.Cases {
		events := make([]map[string]any, 0, len(c.Events))
		for _, spec := range c.Events {
			raw, e := json.Marshal(f.Base[spec.Base])
			if e != nil {
				t.Fatal(e)
			}
			var event map[string]any
			if json.Unmarshal(raw, &event) != nil || event == nil {
				t.Fatal("fixture base")
			}
			for key, value := range spec.Patch {
				event[key] = value
			}
			for _, key := range spec.Omit {
				delete(event, key)
			}
			events = append(events, event)
		}
		t.Run(c.Name, func(t *testing.T) {
			before, _ := json.Marshal(events)
			result, e := BindAuditDecision(f.ProductID, f.Environment, events)
			if !c.Valid {
				if !errors.Is(e, ErrAuditUnavailable) {
					t.Fatal(e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			encoded, e := json.Marshal(result)
			if e != nil {
				t.Fatal(e)
			}
			var observed map[string]any
			if json.Unmarshal(encoded, &observed) != nil {
				t.Fatal("binding JSON")
			}
			if !reflect.DeepEqual(observed, c.Expected) {
				t.Fatal(observed, c.Expected)
			}
			after, _ := json.Marshal(events)
			if string(before) != string(after) {
				t.Fatal("mutated history")
			}
		})
	}
	if _, e = BindAuditDecision("ledger\n", f.Environment, []map[string]any{f.Base["request"]}); !errors.Is(e, ErrInvalidArgument) {
		t.Fatal(e)
	}
}
