package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func readJSON(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSharedSchemas(t *testing.T) {
	root := "../../../../contracts"
	files, err := filepath.Glob(filepath.Join(root, "*.schema.json"))
	if err != nil || len(files) == 0 {
		t.Fatal("missing schemas", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			c := jsonschema.NewCompiler()
			doc := readJSON(t, file)
			id := doc.(map[string]any)["$id"].(string)
			if err := c.AddResource(id, doc); err != nil {
				t.Fatal(err)
			}
			schema, err := c.Compile(id)
			if err != nil {
				t.Fatal(err)
			}
			name := strings.TrimSuffix(filepath.Base(file), ".schema.json")
			data, err := os.ReadFile(filepath.Join(root, "fixtures/schemas", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var cases []struct {
				Name  string
				Value any
				Valid bool
			}
			if err := json.Unmarshal(data, &cases); err != nil {
				t.Fatal(err)
			}
			positive, negative := false, false
			for _, item := range cases {
				if item.Valid {
					positive = true
				} else {
					negative = true
				}
				t.Run(item.Name, func(t *testing.T) {
					err := schema.Validate(item.Value)
					if (err == nil) != item.Valid {
						t.Fatalf("valid=%v, error=%v", item.Valid, err)
					}
				})
			}
			if !positive || !negative {
				t.Fatal("need positive and negative cases")
			}
		})
	}
}
