package identity

import (
	"encoding/json"
	"os"
	"testing"
)

func TestIdentityContext(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/identity-context.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Context, Key, Revision string
		Valid                  bool
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Context, func(t *testing.T) {
			p, e := ParseIdentityContext(c.Context)
			if (e == nil) != c.Valid {
				t.Fatalf("validity: %v", e)
			}
			if c.Valid && c.Key != "" {
				if p == nil || p.Key != c.Key || p.Revision != c.Revision {
					t.Fatal("wrong context")
				}
				built, e := SessionIdentityContext(p.Key, p.Revision)
				if e != nil || built != c.Context {
					t.Fatal("wrong encoding")
				}
			}
		})
	}
}
