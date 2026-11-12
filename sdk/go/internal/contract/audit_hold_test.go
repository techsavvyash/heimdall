package contract

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestAuditHoldBinding(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-hold-binding.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name           string
		Request        AuditHoldRequest
		Administration map[string]any
		Valid          bool
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			before, _ := json.Marshal(c)
			err := ValidateAuditHold(c.Request, c.Administration)
			if (err == nil) != c.Valid || (err != nil && !errors.Is(err, ErrInvalidArgument)) {
				t.Fatal(err)
			}
			after, _ := json.Marshal(c)
			if string(before) != string(after) {
				t.Fatal("mutated input")
			}
		})
	}
}
