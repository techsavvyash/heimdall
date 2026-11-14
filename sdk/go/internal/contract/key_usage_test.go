package contract

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRecordKeyUsage(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/fixtures/record-key-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Vectors                    []struct{ Name, KeyHex, Fingerprint string }
		ValidLimits, InvalidLimits []int64
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			key, err := hex.DecodeString(v.KeyHex)
			if err != nil {
				t.Fatal(err)
			}
			got, err := RecordKeyFingerprint(key)
			if err != nil || got != v.Fingerprint {
				t.Fatalf("fingerprint %s %v", got, err)
			}
			for _, limit := range f.ValidLimits {
				if err := ValidateKeyUsage(got, limit); err != nil {
					t.Fatal(err)
				}
			}
			for _, limit := range f.InvalidLimits {
				if ValidateKeyUsage(got, limit) != ErrInvalidArgument {
					t.Fatal("accepted limit")
				}
			}
		})
	}
	for _, n := range []int{0, 31, 33} {
		if _, err := RecordKeyFingerprint(make([]byte, n)); err != ErrInvalidArgument {
			t.Fatal("accepted key")
		}
	}
	for _, v := range []string{"", strings.Repeat("A", 64), strings.Repeat("0", 63), strings.Repeat("0", 64) + "\n"} {
		if ValidateKeyUsage(v, 1) != ErrInvalidArgument {
			t.Fatal("accepted fingerprint")
		}
	}
}
