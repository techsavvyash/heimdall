package contract

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestEnterpriseEvidence(t *testing.T) {
	type fixture struct {
		Name          string
		Evidence      json.RawMessage
		Scope         EnterpriseScope
		Now, MaxAgeMs int64
		Error         string
	}
	var cases []fixture
	for _, name := range []string{"enterprise-evidence-v1.json", "enterprise-two-tenants-v1.json"} {
		data, err := os.ReadFile("../../../../contracts/fixtures/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var batch []fixture
		if err := json.Unmarshal(data, &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			t.Fatal("empty fixtures: " + name)
		}
		cases = append(cases, batch...)
	}
	schema := payloadSchema(t, "enterprise-evidence")
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var e *EnterpriseEvidence
			if string(c.Evidence) != "null" {
				checkShape(t, schema, c.Evidence)
			}
			if err := json.Unmarshal(c.Evidence, &e); err != nil {
				t.Fatal(err)
			}
			err := ValidateEnterpriseEvidence(e, c.Scope, c.Now, c.MaxAgeMs)
			if c.Error != "" {
				if !errors.Is(err, NewError(Code(c.Error), "")) {
					t.Fatalf("want %s, got %v", c.Error, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
