package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestRetentionUpgradeInput(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/audit-retention-upgrade.json")
	if e != nil {
		t.Fatal(e)
	}
	var f capacityUpgradeFixture
	if json.Unmarshal(raw, &f) != nil || len(f.Files) != 6 {
		t.Fatal("fixture")
	}
	var files [6][]byte
	for i, item := range f.Files {
		files[i], e = os.ReadFile(filepath.Join("../../../../contracts/persistence/session", item.Path))
		if e != nil {
			t.Fatal(e)
		}
		digest := sha256.Sum256(files[i])
		if hex.EncodeToString(digest[:]) != item.Checksum || item.Checksum != retentionUpgradeChecksums[i] {
			t.Fatal("SQL checksum")
		}
	}
	for _, pair := range [][2]string{{auditRetentionSchemaSQL, f.SchemaSQLChecksum}, {auditRetentionSchemaJSON, f.SignatureChecksum}} {
		digest := sha256.Sum256([]byte(pair[0]))
		if hex.EncodeToString(digest[:]) != pair[1] {
			t.Fatal("catalog checksum")
		}
	}
	db, _ := sql.Open("postgres", "")
	defer db.Close()
	ctx := context.Background()
	for i := range files {
		altered := files
		altered[i] = append(append([]byte{}, files[i]...), ' ')
		if e = PrepareAuditRetentionUpgrade(ctx, db, altered, 1000, 8192000, true); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	for _, limits := range [][2]int64{{0, 8192}, {1099511627776, 8192}, {1, 8191}, {1, maxSafe + 1}} {
		if e = PrepareAuditRetentionUpgrade(ctx, db, files, limits[0], limits[1], true); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(e)
		}
	}
	if e = PrepareAuditRetentionUpgrade(nil, db, files, 1000, 8192000, true); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if e = PrepareAuditRetentionUpgrade(ctx, nil, files, 1000, 8192000, true); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	if e = PrepareAuditRetentionUpgrade(ctx, db, files, 1000, 8192000, false); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if e = PrepareAuditRetentionUpgrade(ctx, db, files, 1000, 8192000, true); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
}

func TestRetentionPreparedReadiness(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETENTION_PREPARED_URL")
	if url == "" {
		t.Skip("prepared retention database required")
	}
	db, e := sql.Open("postgres", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	for _, check := range []func(context.Context, *sql.DB) error{CheckCapacityReadiness, CheckManagedCapacityReadiness} {
		if e = check(context.Background(), db); !errors.Is(e, contract.ErrUnavailable) {
			t.Fatal(e)
		}
	}
}
