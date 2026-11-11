package contract

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

func TestTenantModel(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/fixtures/tenant-model-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string
		Manifest Manifest
		Binding  Binding
		Expected TenantModel
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			actual, err := CompileTenantModel(c.Manifest, c.Binding)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, c.Expected) {
				t.Fatal("model or hash mismatch")
			}
			slices.Reverse(c.Manifest.Permissions)
			again, err := CompileTenantModel(c.Manifest, c.Binding)
			if err != nil || !reflect.DeepEqual(actual, again) {
				t.Fatal("order changed model")
			}
			for role, want := range c.Expected.Roles {
				got, err := ResolveRoleRelations(actual, role)
				if err != nil || got != want {
					t.Fatal("role resolution mismatch")
				}
			}
			for _, role := range []string{"", "constructor", "__proto__", "OWNER", "unknown"} {
				if _, err := ResolveRoleRelations(actual, role); err != ErrInvalidArgument {
					t.Fatal("unknown role accepted")
				}
			}
			c.Manifest.Delegation[c.Manifest.OwnerRole] = []string{c.Manifest.OwnerRole}
			if _, err := CompileTenantModel(c.Manifest, c.Binding); err != ErrInvalidArgument {
				t.Fatal("owner delegation accepted")
			}
			c.Manifest.OwnerRole = "missing"
			if _, err := CompileTenantModel(c.Manifest, c.Binding); err != ErrInvalidArgument {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}
