package contract

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestIdentityVectors(t *testing.T) {
	data, err := os.ReadFile("../../../../contracts/fixtures/identity/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name, Issuer, Subject, Expected, Error string
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) == 0 {
		t.Fatal("empty identity fixture corpus")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			got, err := EncodeIdentity(v.Issuer, v.Subject)
			if v.Error != "" {
				if !errors.Is(err, ErrInvalidArgument) || v.Error != ErrInvalidArgument.Error() {
					t.Fatalf("expected %s, got %v", v.Error, err)
				}
			} else if err != nil || got != v.Expected {
				t.Fatalf("expected %q, got %q (%v)", v.Expected, got, err)
			}
		})
	}
}

func TestMalformedUTF8(t *testing.T) {
	for _, value := range []string{string([]byte{0xff}), string([]byte{0xed, 0xa0, 0x80})} {
		if _, err := EncodeIdentity("https://id.example.com", value); !errors.Is(err, ErrInvalidArgument) {
			t.Fatal("accepted malformed subject")
		}
		if _, err := EncodeIdentity(value, "maya"); !errors.Is(err, ErrInvalidArgument) {
			t.Fatal("accepted malformed issuer")
		}
	}
}
