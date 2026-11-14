package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestAuditCapacitySchemaChecksum(t *testing.T) {
	const root = "../../../../contracts/"
	raw, err := os.ReadFile(root + "fixtures/audit-capacity-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Path, Checksum string }
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(root + "persistence/" + f.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != f.Checksum {
		t.Fatal("capacity SQL checksum differs")
	}
}
