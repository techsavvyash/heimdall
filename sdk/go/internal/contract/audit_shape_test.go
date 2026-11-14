package contract

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"os"
	"strings"
	"testing"
)

func TestAuditShape(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/schemas/audit-event.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name  string
		Value any
		Valid bool
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixtures")
	}
	var base map[string]any
	for _, c := range cases {
		m, _ := c.Value.(map[string]any)
		if c.Valid && base == nil {
			base = m
		}
		if (ValidateAuditShape(m) == nil) != c.Valid {
			t.Fatal(c.Name)
		}
	}
	raw, e = os.ReadFile("../../../../contracts/audit-event.schema.json")
	if e != nil {
		t.Fatal(e)
	}
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil {
		t.Fatal("schema")
	}
	compiler := jsonschema.NewCompiler()
	if e = compiler.AddResource("https://test/audit", schema); e != nil {
		t.Fatal(e)
	}
	shape, e := compiler.Compile("https://test/audit")
	if e != nil {
		t.Fatal(e)
	}
	for key := range schema["properties"].(map[string]any) {
		t.Run(key, func(t *testing.T) {
			for _, value := range []any{nil, true, false, map[string]any{}, []any{}, []any{float64(1)}, float64(0), float64(1), float64(-1), 1.5, float64(2147483648), float64(9007199254740991), "", "valid", "user", "unknown", "system", "succeeded", "heimdall.test", "heimdall.test\n", "id\n", "東京", strings.Repeat("🧭", 1024), strings.Repeat("x", 8193)} {
				event := map[string]any{}
				for k, v := range base {
					event[k] = v
				}
				event[key] = value
				if (ValidateAuditShape(event) == nil) != (shape.Validate(event) == nil) {
					t.Fatalf("schema mismatch %s %v", key, value)
				}
			}
		})
	}
	for _, key := range schema["required"].([]any) {
		event := map[string]any{}
		for k, v := range base {
			event[k] = v
		}
		delete(event, key.(string))
		if ValidateAuditShape(event) == nil {
			t.Fatal("missing", key)
		}
	}
	raw, e = os.ReadFile("../../../../contracts/fixtures/audit-shape-boundaries.json")
	if e != nil {
		t.Fatal(e)
	}
	var boundaries []struct {
		Name   string
		Path   []string
		Value  any
		Repeat string
		Count  int
		Valid  bool
	}
	if json.Unmarshal(raw, &boundaries) != nil {
		t.Fatal("boundary fixtures")
	}
	for _, c := range boundaries {
		t.Run(c.Name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var event map[string]any
			_ = json.Unmarshal(raw, &event)
			target := event
			for _, key := range c.Path[:len(c.Path)-1] {
				target = target[key].(map[string]any)
			}
			value := c.Value
			if c.Repeat != "" {
				value = strings.Repeat(c.Repeat, c.Count)
			}
			target[c.Path[len(c.Path)-1]] = value
			if (ValidateAuditShape(event) == nil) != c.Valid || (shape.Validate(event) == nil) != c.Valid {
				t.Fatal("boundary mismatch")
			}
		})
	}

}
