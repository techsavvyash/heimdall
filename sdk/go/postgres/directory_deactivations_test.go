package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

func TestDirectoryDeactivationPostgres(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_DIRECTORY_DB")
	if dsn == "" {
		t.Skip("activated PostgreSQL required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var wrongOwner atomic.Bool
	var initial atomic.Bool
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner := "org-a"
		if wrongOwner.Load() {
			owner = "org-b"
		}
		state := "USER_STATE_INACTIVE"
		if initial.Load() {
			state = "USER_STATE_INITIAL"
		}
		if deleted.Load() {
			state = "USER_STATE_DELETED"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{
			"userId": "employee", "state": state, "human": map[string]any{},
			"details": map[string]string{"resourceOwner": owner},
		}})
	}))
	defer server.Close()
	const product, environment, other = "ledger", "directory-go", "directory-other-go"
	const rollback = "directory-rollback-go"
	identity := heimdall.Identity{Issuer: server.URL, Subject: "employee"}
	options := provisioning.DirectoryReaderOptions{Issuer: server.URL, OrganizationID: "org-a", Token: "viewer", TransportProfile: "loopback"}
	observe, err := NewDirectoryDeactivation(db, product, environment, options)
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewStorage(db, StorageOptions{ProductID: product, Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	encoded, err := contract.EncodeIdentity(identity.Issuer, identity.Subject)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(encoded))
	for _, scope := range []string{environment, other, rollback} {
		if _, err := db.ExecContext(ctx, "INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at,identity_hash) VALUES($1,$2,'session',$3,'fixture','test',0,$3)", product, scope, hash[:]); err != nil {
			t.Fatal(err)
		}
	}
	faulty, err := NewDirectoryDeactivation(db, product, rollback, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := faulty(ctx, identity.Subject); !errors.Is(err, heimdall.ErrUnavailable) {
		t.Fatal("failed session deletion", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM heimdall.identity_denials WHERE environment=$1)
		+ (SELECT count(*) FROM heimdall_directory.deactivations WHERE environment=$1)`, rollback).Scan(&count); err != nil || count != 0 {
		t.Fatal("deactivation transaction failed to roll back", count, err)
	}
	wrongOwner.Store(true)
	if _, err := observe(ctx, identity.Subject); !errors.Is(err, heimdall.ErrConflict) {
		t.Fatal("wrong provider organization", err)
	}
	if denied, err := port.Identity.Check(ctx, identity); err != nil || denied {
		t.Fatal("unexpected deny", denied, err)
	}
	wrongOwner.Store(false)
	for _, count := range []int64{1, 0} {
		result, err := observe(ctx, identity.Subject)
		if err != nil || result != (DirectoryDeactivationResult{"inactive", count}) {
			t.Fatal(result, err)
		}
	}
	initial.Store(true)
	if result, err := observe(ctx, identity.Subject); err != nil || result != (DirectoryDeactivationResult{"initial", 0}) {
		t.Fatal(result, err)
	}
	if denied, err := port.Identity.Check(ctx, identity); err != nil || !denied {
		t.Fatal("deny missing", denied, err)
	}
	initial.Store(false)
	wrongOwner.Store(true)
	options.OrganizationID = "org-b"
	otherObserver, err := NewDirectoryDeactivation(db, product, environment, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherObserver(ctx, identity.Subject); !errors.Is(err, heimdall.ErrConflict) {
		t.Fatal("intent rebound", err)
	}
	var owner, reason string
	if err := db.QueryRowContext(ctx, "SELECT organization_id,reason FROM heimdall_directory.deactivations WHERE product_id=$1 AND environment=$2", product, environment).Scan(&owner, &reason); err != nil || owner != "org-a" || reason != "inactive" {
		t.Fatal("intent missing", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM heimdall.records WHERE product_id=$1 AND identity_hash=$2 AND environment=$3", product, hash[:], other).Scan(&count); err != nil || count != 1 {
		t.Fatal("other product environment changed", err)
	}
	cleanup, err := NewDirectoryCleanup(db, product, environment, server.URL, "org-a", "loopback")
	if err != nil {
		t.Fatal(err)
	}
	items, err := cleanup.Pending(ctx, "", 1)
	if err != nil || len(items) != 1 || items[0].Subject != identity.Subject || items[0].Reason != "inactive" {
		t.Fatal("pending cleanup missing", items, err)
	}
	revision := items[0].Revision
	if items, err := cleanup.Pending(ctx, revision, 1); err != nil || len(items) != 0 {
		t.Fatal("cursor repeated item", items, err)
	}
	isolated, err := NewDirectoryCleanup(db, product, environment, server.URL, "org-b", "loopback")
	if err != nil {
		t.Fatal(err)
	}
	if items, err := isolated.Pending(ctx, "", 1); err != nil || len(items) != 0 {
		t.Fatal("cross-organization read", items, err)
	}
	for range 2 {
		if err := cleanup.Complete(ctx, identity.Subject, revision); err != nil {
			t.Fatal("acknowledgement failed", err)
		}
	}
	wrongOwner.Store(false)
	if _, err := observe(ctx, identity.Subject); err != nil {
		t.Fatal(err)
	}
	if items, err := cleanup.Pending(ctx, "", 1); err != nil || len(items) != 0 {
		t.Fatal("replay reopened item", items, err)
	}
	deleted.Store(true)
	if _, err := observe(ctx, identity.Subject); err != nil {
		t.Fatal(err)
	}
	if err := cleanup.Complete(ctx, identity.Subject, revision); !errors.Is(err, heimdall.ErrConflict) {
		t.Fatal("stale acknowledgement accepted", err)
	}
	items, err = cleanup.Pending(ctx, "", 1)
	if err != nil || len(items) != 1 || items[0].Reason != "deleted" || items[0].Revision == revision {
		t.Fatal("deletion did not supersede inactive item", items, err)
	}
	if denied, err := port.Identity.Check(ctx, identity); err != nil || !denied {
		t.Fatal("deny missing after acknowledgement", denied, err)
	}
}
