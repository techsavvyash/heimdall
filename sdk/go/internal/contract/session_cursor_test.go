package contract

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func payloadSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	doc := readJSON(t, "../../../../contracts/"+name+".schema.json")
	c := jsonschema.NewCompiler()
	id := doc.(map[string]any)["$id"].(string)
	files, err := filepath.Glob("../../../../contracts/*.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		resource := readJSON(t, file)
		if err := c.AddResource(resource.(map[string]any)["$id"].(string), resource); err != nil {
			t.Fatal(err)
		}
	}

	s, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func checkShape(t *testing.T, s *jsonschema.Schema, raw json.RawMessage) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(value); err != nil {
		t.Fatal(err)
	}
}
func TestSessionSemantics(t *testing.T) {
	data, err := os.ReadFile("../../../../contracts/fixtures/session-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name             string
		Record           json.RawMessage
		Scope            SessionScope
		Now, FreshnessMs int64
		Expected, Error  string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	schema := payloadSchema(t, "session-payload")
	if len(cases) == 0 {
		t.Fatal("empty fixtures")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			checkShape(t, schema, c.Record)
			var r SessionRecord
			if err := json.Unmarshal(c.Record, &r); err != nil {
				t.Fatal(err)
			}
			got, err := SessionStatus(r, c.Scope, c.Now, c.FreshnessMs)
			if c.Error != "" {
				if !errors.Is(err, NewError(Code(c.Error), "")) {
					t.Fatalf("expected %s, got %v", c.Error, err)
				}
			} else if err != nil || got != c.Expected {
				t.Fatalf("expected %s, got %s (%v)", c.Expected, got, err)
			}
		})
	}
}
func TestCursorSemantics(t *testing.T) {
	data, err := os.ReadFile("../../../../contracts/fixtures/cursor-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name    string
		Payload json.RawMessage
		Scope   CursorScope
		Now     int64
		Valid   bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	schema := payloadSchema(t, "cursor-payload")
	if len(cases) == 0 {
		t.Fatal("empty fixtures")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			checkShape(t, schema, c.Payload)
			var p CursorPayload
			if err := json.Unmarshal(c.Payload, &p); err != nil {
				t.Fatal(err)
			}
			err := ValidateCursorScope(p, c.Scope, c.Now)
			if c.Valid && err != nil {
				t.Fatal(err)
			}
			if !c.Valid && !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("expected invalid_argument, got %v", err)
			}
		})
	}
}
