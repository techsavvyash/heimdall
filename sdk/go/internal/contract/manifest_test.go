package contract

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestManifestBinding(t *testing.T) {
	const root = "../../../../contracts/"
	data, err := os.ReadFile(root + "fixtures/manifest-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string
		Manifest json.RawMessage
		Config   json.RawMessage
		Valid    bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"manifest", "config"} {
		c := jsonschema.NewCompiler()
		doc := readJSON(t, root+name+".schema.json")
		id := doc.(map[string]any)["$id"].(string)
		if err := c.AddResource(id, doc); err != nil {
			t.Fatal(err)
		}
		schema, err := c.Compile(id)
		if err != nil {
			t.Fatal(err)
		}
		schemas[name] = schema
	}
	if len(cases) == 0 {
		t.Fatal("empty fixture corpus")
	}
	for _, v := range cases {
		t.Run(v.Name, func(t *testing.T) {
			for name, raw := range map[string]json.RawMessage{"manifest": v.Manifest, "config": v.Config} {
				var value any
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				if err := schemas[name].Validate(value); err != nil {
					t.Fatalf("invalid structural input: %v", err)
				}
			}
			var m Manifest
			var c Binding
			if err := json.Unmarshal(v.Manifest, &m); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(v.Config, &c); err != nil {
				t.Fatal(err)
			}
			err := ValidateManifestBinding(m, c)
			if v.Valid && err != nil {
				t.Fatal(err)
			}
			if !v.Valid && !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("expected invalid_argument, got %v", err)
			}
		})
	}
}
