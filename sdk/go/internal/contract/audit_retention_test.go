package contract

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAuditRetention(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-retention.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name        string
		Scope       RetentionScope
		Group       RetentionGroup
		Now         int64
		RetainForMS int64 `json:"retainForMs"`
		Expected    string
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	if DefaultAuditRetentionMS != 2592000000 {
		t.Fatal("default")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			before, _ := json.Marshal(c)
			got, e := ClassifyAuditRetention(c.Scope, c.Group, c.Now, c.RetainForMS)
			if e != nil {
				got = e.Error()
			}
			if got != c.Expected {
				t.Fatal(got, e)
			}
			after, _ := json.Marshal(c)
			if string(before) != string(after) {
				t.Fatal("mutated input")
			}
		})
	}
}
