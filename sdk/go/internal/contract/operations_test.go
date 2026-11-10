package contract

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestIntentDigests(t *testing.T) {
	data, err := os.ReadFile("../../../../contracts/fixtures/operation-intent-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, IdempotencyKey, Fingerprint, KeyDigest string
		Intent                                       json.RawMessage
		Valid                                        bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	schema := payloadSchema(t, "operation-intent")
	if len(cases) == 0 {
		t.Fatal("empty fixtures")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			checkShape(t, schema, c.Intent)
			var intent Intent
			if err := json.Unmarshal(c.Intent, &intent); err != nil {
				t.Fatal(err)
			}
			got, err := IntentDigests(intent, c.IdempotencyKey)
			if c.Valid {
				if err != nil || got.Fingerprint != c.Fingerprint || got.KeyDigest != c.KeyDigest {
					t.Fatalf("digest mismatch: %+v (%v)", got, err)
				}
			} else if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("expected invalid_argument, got %v", err)
			}
		})
	}
}
func TestOperationTransitions(t *testing.T) {
	data, err := os.ReadFile("../../../../contracts/fixtures/operation-state-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, State, Event, Expected, Error           string
		DispatchCount, Generation, ExpectedGeneration int64
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty fixtures")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := NextOperationState(c.State, c.Event, c.DispatchCount, c.Generation, c.ExpectedGeneration)
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
