package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestAuditRetentionIdentitiesChecksum(t *testing.T) {
	const root = "../../../../contracts/"
	raw, err := os.ReadFile(root + "fixtures/audit-retention-identities.json")
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
		t.Fatal("retention identity SQL checksum differs")
	}
}
