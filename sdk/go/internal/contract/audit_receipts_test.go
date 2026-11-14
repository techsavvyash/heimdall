package contract

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

func readAuditFixture(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile("../../../../contracts/fixtures/" + name + "-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
func TestAuditDigests(t *testing.T) {
	var cases []struct {
		Name, Digest string
		Event        map[string]any
	}
	readAuditFixture(t, "audit-digests", &cases)
	schema := payloadSchema(t, "audit-event")
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if err := schema.Validate(c.Event); err != nil {
				t.Fatal(err)
			}
			got, err := AuditDigest(c.Event)
			if err != nil || got != c.Digest {
				t.Fatalf("got %s, %v; want %s", got, err, c.Digest)
			}
		})
	}
	var nested any
	for i := 0; i < 17; i++ {
		nested = []any{nested}
	}
	for _, v := range []any{math.NaN(), math.Inf(1), float64(9007199254740992), "\xff", map[string]any{"non ascii é": float64(1)}, map[string]any{strings.Repeat("k", 65): float64(1)}, strings.Repeat("x", 1048576), nested} {
		if _, err := AuditDigest(map[string]any{"value": v}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	}
}
func TestAuditReceipts(t *testing.T) {
	var cases []struct {
		Name               string
		Expected, Received AuditReceipt
		Valid              bool
	}
	readAuditFixture(t, "audit-receipts", &cases)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			err := ValidateAuditReceipt(c.Expected, c.Received)
			if (c.Valid && err != nil) || (!c.Valid && !errors.Is(err, ErrAuditUnavailable)) {
				t.Fatalf("unexpected receipt result %v", err)
			}
		})
	}
	var deliveries []struct {
		Name           string
		Batch, Receipt AuditDelivery
		Valid          bool
	}
	readAuditFixture(t, "audit-delivery", &deliveries)
	for _, c := range deliveries {
		t.Run(c.Name, func(t *testing.T) {
			err := ValidateDeliveryReceipt(c.Batch, c.Receipt)
			if (c.Valid && err != nil) || (!c.Valid && !errors.Is(err, ErrUnavailable)) {
				t.Fatalf("unexpected delivery result %v", err)
			}
		})
	}
}
