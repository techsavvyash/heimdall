package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestMigrationCatalog(t *testing.T) {
	const root = "../../../../contracts/persistence/"
	raw, err := os.ReadFile(root + "catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	type entry struct {
		Version        int
		Path, Checksum string
	}
	var catalog struct {
		FormatVersion    int
		Session, Managed []entry
	}
	if err = json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.FormatVersion != 1 || len(catalog.Session) == 0 || len(catalog.Managed) == 0 {
		t.Fatal("invalid catalog")
	}
	for family, entries := range map[string][]entry{"session": catalog.Session, "managed": catalog.Managed} {
		t.Run(family, func(t *testing.T) {
			migrations := []MigrationChecksum{}
			for index, m := range entries {
				if m.Version != index+1 || !regexp.MustCompile(`^`+family+`/[0-9]{4}\.sql$`).MatchString(m.Path) {
					t.Fatal("invalid migration entry")
				}
				sql, err := os.ReadFile(root + m.Path)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(sql)
				if bytes.ContainsRune(sql, '\r') || hex.EncodeToString(sum[:]) != m.Checksum {
					t.Fatal("SQL bytes differ from checksum")
				}
				migrations = append(migrations, MigrationChecksum{Version: m.Version, Checksum: m.Checksum})
			}
			metadata := StorageMetadata{DatabaseVersion: len(migrations), State: "ready", Readers: ProtocolRange{1, 1}, Writers: ProtocolRange{1, 1}, Migrations: migrations}
			policy := StoragePolicy{ReaderVersion: 1, WriterVersion: 1, MinDatabaseVersion: 1, MaxDatabaseVersion: len(migrations), Migrations: migrations}
			if err := ValidateStorageCompatibility(metadata, policy); err != nil {
				t.Fatal(err)
			}
			metadata.Migrations = []MigrationChecksum{{Version: 1, Checksum: "0000000000000000000000000000000000000000000000000000000000000000"}}
			if err := ValidateStorageCompatibility(metadata, policy); err != ErrUnavailable {
				t.Fatal("altered migration accepted")
			}
		})
	}
}
