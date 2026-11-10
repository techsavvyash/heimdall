package contract

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestAuditContract(t *testing.T) {
	const root = "../../../../contracts/"
	data, err := os.ReadFile(root + "fixtures/audit-types-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var types map[string][]string
	if err := json.Unmarshal(data, &types); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(types, AuditTypes) {
		t.Fatal("event catalog differs")
	}
	data, err = os.ReadFile(root + "fixtures/audit-registry-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var registry map[string]struct {
		Version int
		Schema  any
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	compiled := map[string]*jsonschema.Schema{}
	for name, definition := range registry {
		c := jsonschema.NewCompiler()
		id := "https://fixtures.example/" + name
		if err := c.AddResource(id, definition.Schema); err != nil {
			t.Fatal(err)
		}
		s, err := c.Compile(id)
		if err != nil {
			t.Fatal(err)
		}
		compiled[name] = s
	}
	custom := func(name string, version int, attrs map[string]any) bool {
		def, ok := registry[name]
		return ok && def.Version == version && compiled[name].Validate(attrs) == nil
	}
	data, err = os.ReadFile(root + "fixtures/audit-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Origin string
		Event        json.RawMessage
		Valid        bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	schema := payloadSchema(t, "audit-event")
	if len(cases) == 0 {
		t.Fatal("empty fixtures")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			checkShape(t, schema, c.Event)
			var e AuditEvent
			if err := json.Unmarshal(c.Event, &e); err != nil {
				t.Fatal(err)
			}
			err := ValidateAuditEvent(e, c.Origin, custom)
			if c.Valid && err != nil {
				t.Fatal(err)
			}
			if !c.Valid && !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("expected invalid_argument, got %v", err)
			}
		})
	}
}
