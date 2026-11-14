package contract

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAuditRetirementBinding(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-retire-storage.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Request        json.RawMessage
			Administration map[string]any
		}
		BindingMutations []struct {
			Name    string
			Request json.RawMessage
			Event   map[string]any
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	c := fixture.Cases[0]
	var valid AuditRetirementRequest
	if e = json.Unmarshal(c.Request, &valid); e != nil {
		t.Fatal(e)
	}
	if e = ValidateAuditRetirement(valid, c.Administration); e != nil {
		t.Fatal(e)
	}
	for _, m := range fixture.BindingMutations {
		t.Run(m.Name, func(t *testing.T) {
			r := valid
			if len(m.Request) > 0 {
				if err := json.Unmarshal(m.Request, &r); err != nil {
					t.Fatal(err)
				}
			}
			event := make(map[string]any, len(c.Administration))
			for k, v := range c.Administration {
				event[k] = v
			}
			for k, v := range m.Event {
				event[k] = v
			}
			if err := ValidateAuditRetirement(r, event); err != ErrInvalidArgument {
				t.Fatal(err)
			}
		})
	}
}
