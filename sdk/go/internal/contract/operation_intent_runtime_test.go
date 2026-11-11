package contract

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestIntentRuntimeParser(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/operation-intent-runtime.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name, JSON string
		Valid      bool
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixtures")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			i, e := ParseIntent([]byte(c.JSON))
			if !c.Valid {
				if !errors.Is(e, ErrInvalidArgument) {
					t.Fatal(i, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = IntentDigests(i, "key"); e != nil {
				t.Fatal(e)
			}
			encoded, e := json.Marshal(i)
			if e != nil {
				t.Fatal(e)
			}
			reparsed, e := ParseIntent(encoded)
			if e != nil || !reflect.DeepEqual(i, reparsed) {
				t.Fatal("roundtrip", e)
			}
		})
	}
	base := cases[0].JSON
	if _, e = ParseIntent([]byte(strings.Repeat(" ", 1048577) + base)); !errors.Is(e, ErrInvalidArgument) {
		t.Fatal(e)
	}
	if _, e = ParseIntent([]byte(strings.Repeat(" ", 1048576-len(base)) + base)); e != nil {
		t.Fatal(e)
	}
	if _, e = ParseIntent([]byte(strings.Replace(base, "Acme", string([]byte{0xff}), 1))); !errors.Is(e, ErrInvalidArgument) {
		t.Fatal(e)
	}
	i, e := ParseIntent([]byte(base))
	if e != nil {
		t.Fatal(e)
	}
	i.Kind = "assign_roles"
	i.Name = ""
	i.WorkspaceID = "acme"
	i.Roles = []string{"viewer", "viewer"}
	if _, e = IntentDigests(i, "key"); !errors.Is(e, ErrInvalidArgument) {
		t.Fatal(e)
	}
}
func TestIntentRuntimeDigestParity(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/operation-intent-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Intent                                 json.RawMessage
		IdempotencyKey, Fingerprint, KeyDigest string
		Valid                                  bool
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixture")
	}
	for _, c := range cases {
		if !c.Valid {
			continue
		}
		i, e := ParseIntent(c.Intent)
		if e != nil {
			t.Fatal(e)
		}
		d, e := IntentDigests(i, c.IdempotencyKey)
		if e != nil || d.Fingerprint != c.Fingerprint || d.KeyDigest != c.KeyDigest {
			t.Fatal(d, e)
		}
	}
}

func TestIntentJournalBinding(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/operation-intent-binding.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name, JSON string
		Binding    IntentBinding
		Valid      bool
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("fixture")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			i, e := ParseBoundIntent([]byte(c.JSON), c.Binding)
			if !c.Valid {
				if !errors.Is(e, ErrUnavailable) {
					t.Fatal(i, e)
				}
				return
			}
			expected, _ := ParseIntent([]byte(c.JSON))
			if e != nil || !reflect.DeepEqual(i, expected) {
				t.Fatal(i, e)
			}
		})
	}
}
