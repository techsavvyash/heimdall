package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

func TestLiveSCIMWorker(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_LIVE_SCIM")
	if raw == "" {
		t.Skip("requires live SCIM verification")
	}
	var c struct {
		provisioning.DirectoryReaderOptions
		Subject, Stage string
	}
	if json.Unmarshal([]byte(raw), &c) != nil {
		t.Fatal("invalid live SCIM test configuration")
	}
	db, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DIRECTORY_DB"))
	if err != nil {
		t.Fatal("invalid verification database")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	const product, environment, other = "support", "scim-live-go", "scim-live-other-go"
	worker, err := NewDirectoryPolling(db, product, environment, c.DirectoryReaderOptions)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := NewStorage(db, StorageOptions{ProductID: product, Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := NewDirectoryCleanup(db, product, environment, c.Issuer, c.OrganizationID, "production")
	if err != nil {
		t.Fatal(err)
	}
	identity := heimdall.Identity{Issuer: c.Issuer, Subject: c.Subject}
	encoded, err := contract.EncodeIdentity(identity.Issuer, identity.Subject)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(encoded))
	if c.Stage == "seed" {
		for _, scope := range []string{environment, other} {
			if _, err := db.ExecContext(ctx, "INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at,identity_hash) VALUES($1,$2,'session',$3,'fixture','fixture',0,$3)", product, scope, hash[:]); err != nil {
				t.Fatal(err)
			}
		}
		discovered, err := worker.Discover(ctx, 50)
		if err != nil || discovered.Discovered != 1 || !discovered.CycleComplete {
			t.Fatal("unexpected live directory discovery", discovered, err)
		}
	}
	result, err := worker.ObserveKnown(ctx, "", 2)
	wantInvalidated := 0
	if c.Stage == "inactive" {
		wantInvalidated = 1
	}
	if err != nil || result.Observed != 1 || int(result.SessionsInvalidated) != wantInvalidated {
		t.Fatal("unexpected live worker result", result, err)
	}
	if denied, err := storage.Identity.Check(ctx, identity); err != nil || denied != (c.Stage != "seed") {
		t.Fatal("unexpected durable identity gate", denied, err)
	}
	pending, err := cleanup.Pending(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if c.Stage == "seed" {
		if len(pending) != 0 {
			t.Fatal("active user queued cleanup")
		}
	} else {
		reason := "inactive"
		if c.Stage == "missing" {
			reason = "deleted"
		}
		if len(pending) != 1 || pending[0].Reason != reason {
			t.Fatal("expected a scoped cleanup revision")
		}
	}
	var ownRows, otherRows int
	err = db.QueryRowContext(ctx, "SELECT count(*) FILTER (WHERE environment=$3),count(*) FILTER (WHERE environment=$4) FROM heimdall.records WHERE product_id=$1 AND identity_hash=$2", product, hash[:], environment, other).Scan(&ownRows, &otherRows)
	if err != nil || otherRows != 1 || (ownRows == 1) != (c.Stage == "seed") {
		t.Fatal("unexpected scoped session invalidation", ownRows, otherRows, err)
	}
}
