package contract

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestAuditCapacityFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/fixtures/audit-capacity.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Operation string
		Limits, Charge  AuditCapacity
		State           AuditCapacityState
		Expected        json.RawMessage
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			result, err := TransitionAuditCapacity(c.Limits, c.State, c.Operation, c.Charge)
			var code string
			if json.Unmarshal(c.Expected, &code) == nil {
				if err == nil || err.Error() != code {
					t.Fatalf("expected %s, got %v", code, err)
				}
				return
			}
			var expected AuditCapacityState
			if e := json.Unmarshal(c.Expected, &expected); e != nil {
				t.Fatal(e)
			}
			if err != nil || !reflect.DeepEqual(result, expected) {
				t.Fatalf("expected %+v, got %+v %v", expected, result, err)
			}
		})
	}
}
