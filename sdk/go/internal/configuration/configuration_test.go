package configuration

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func read(t *testing.T, path string, target any) {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/" + path)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, target); e != nil {
		t.Fatal(e)
	}
}
func TestShapes(t *testing.T) {
	for _, name := range []string{"config", "manifest"} {
		var cases []struct {
			Name  string
			Value any
			Valid bool
		}
		read(t, "schemas/"+name+".json", &cases)
		for _, c := range cases {
			t.Run(name+"/"+c.Name, func(t *testing.T) {
				validate := ValidateDeploymentShape
				if name == "manifest" {
					validate = ValidateManifestShape
				}
				if (validate(c.Value) == nil) != c.Valid {
					t.Fatal("schema mismatch")
				}
			})
		}
	}
}
func patch(value map[string]any, patches [][]any) {
	for _, p := range patches {
		path := p[0].([]any)
		parent := value
		for _, key := range path[:len(path)-1] {
			parent = parent[key.(string)].(map[string]any)
		}
		parent[path[len(path)-1].(string)] = p[1]
	}
}
func TestConfiguration(t *testing.T) {
	var f struct {
		Config, Manifest map[string]any
		Cases            []struct {
			Name             string
			Valid            bool
			Config, Manifest [][]any
		}
	}
	read(t, "configuration-v1.json", &f)
	cJSON, _ := json.Marshal(f.Config)
	mJSON, _ := json.Marshal(f.Manifest)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var config, manifest map[string]any
			_ = json.Unmarshal(cJSON, &config)
			_ = json.Unmarshal(mJSON, &manifest)
			patch(config, c.Config)
			patch(manifest, c.Manifest)
			cRaw, _ := json.Marshal(config)
			mRaw, _ := json.Marshal(manifest)
			_, e := Parse(cRaw, mRaw)
			if (e == nil) != c.Valid {
				t.Fatalf("binding mismatch: %v", e)
			}
		})
	}
	d, e := Parse(cJSON, mJSON)
	if e != nil {
		t.Fatal(e)
	}
	roundConfig, _ := json.Marshal(d.Config())
	roundManifest, _ := json.Marshal(d.Manifest())
	if _, e = Parse(roundConfig, roundManifest); e != nil {
		t.Fatal("serialized binding changed schema", e)
	}
	original := d.Artifact()
	copy := d.Artifact()
	copy.Permissions["invoice.read"] = "member"
	manifest := d.Manifest()
	manifest.Roles["viewer"] = []string{"changed"}
	config := d.Config()
	config.OpenFGA.ModelID = "changed"
	if !reflect.DeepEqual(d.Artifact(), original) || d.Manifest().Roles["viewer"][0] != "invoice.read" || d.Config().OpenFGA.ModelID == "changed" {
		t.Fatal("mutable alias")
	}
	for _, raw := range []string{"{", "[]", "null", strings.Repeat(" ", 1048577), strings.Replace(string(cJSON), `"ledger-api"`, `"\ud800"`, 1)} {
		if _, e = Parse([]byte(raw), mJSON); e == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, e = Parse([]byte(strings.Replace(string(cJSON), `"schemaVersion":1`, `"schemaVersion":1e0`, 1)), mJSON); e != nil {
		t.Fatal("integer exponent", e)
	}
}
