package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

type liveSCIMConfiguration struct {
	provisioning.DirectoryReaderOptions
	Subject, Stage, Revision, WorkspaceID, ApprovedRole string
	ClientSecret, OpenFGAKey, OperatorToken             string
	Config, Manifest                                    json.RawMessage
}

func TestLiveSCIMWorker(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_LIVE_SCIM")
	if raw == "" {
		t.Skip("requires live SCIM verification")
	}
	var c liveSCIMConfiguration
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
	if c.Stage == "stale-reactivation" {
		rejectStaleSCIMReactivation(t, ctx, db, c)
		return
	}
	if strings.HasPrefix(c.Stage, "cleanup") || c.Stage == "reenroll" {
		verifyLiveSCIMReactivation(t, ctx, db, c)
		return
	}
	if c.Stage == "browser" {
		observe, err := NewDirectoryDeactivation(db, product, "verification", c.DirectoryReaderOptions)
		if err != nil {
			t.Fatal(err)
		}
		result, err := observe(ctx, c.Subject)
		if err != nil || result.State != "inactive" || result.SessionsInvalidated != 1 {
			t.Fatal("SCIM disable did not revoke the browser session", result, err)
		}
		storage, err := NewStorage(db, StorageOptions{ProductID: product, Environment: "verification"})
		if err != nil {
			t.Fatal(err)
		}
		if denied, err := storage.Identity.Check(ctx, heimdall.Identity{Issuer: c.Issuer, Subject: c.Subject}); err != nil || !denied {
			t.Fatal("durable identity denial absent", err)
		}
		return
	}
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

func rejectStaleSCIMReactivation(t *testing.T, ctx context.Context, db *sql.DB, c liveSCIMConfiguration) {
	t.Helper()
	const product, environment = "support", "verification"
	prepare, err := NewDirectoryReactivation(db, product, environment, c.DirectoryReaderOptions)
	if err != nil {
		t.Fatal(err)
	}
	release, err := NewDirectoryReactivationRelease(db, product, environment, c.DirectoryReaderOptions)
	if err != nil {
		t.Fatal(err)
	}
	approval := []DirectoryReactivationEnrollment{{WorkspaceID: c.WorkspaceID, RuleID: "browser-scim-v1"}}
	fmt.Println("SCIM_STEP stale-provider-state")
	if _, err := prepare(ctx, c.Subject, c.Revision); !errors.Is(err, heimdall.ErrForbidden) {
		t.Fatal("expected stale preparation to be denied", err)
	}
	if err := release(ctx, c.Subject, c.Revision, approval); !errors.Is(err, heimdall.ErrForbidden) {
		t.Fatal("expected stale release to be denied", err)
	}
	observe, err := NewDirectoryDeactivation(db, product, environment, c.DirectoryReaderOptions)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := observe(ctx, c.Subject); err != nil || result.State != "inactive" || result.SessionsInvalidated != 1 {
		t.Fatal("expected the later disable to revoke the new session", result, err)
	}
	if err := release(ctx, c.Subject, c.Revision, approval); !errors.Is(err, heimdall.ErrForbidden) {
		t.Fatal("expected stale release to be denied after observation", err)
	}
	storage, err := NewStorage(db, StorageOptions{ProductID: product, Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	if denied, err := storage.Identity.Check(ctx, heimdall.Identity{Issuer: c.Issuer, Subject: c.Subject}); err != nil || !denied {
		t.Fatal("expected later durable denial", err)
	}
}

func verifyLiveSCIMReactivation(t *testing.T, ctx context.Context, db *sql.DB, c liveSCIMConfiguration) {
	t.Helper()
	const product, environment = "support", "verification"
	fmt.Println("SCIM_STEP runtime")
	storage, err := NewManagedStorage(db, StorageOptions{ProductID: product, Environment: environment})
	if err != nil {
		t.Fatal("SCIM_STEP storage-error", err)
	}
	client, err := heimdall.New(ctx, c.Config, c.Manifest, heimdall.Options{
		Storage: storage, PolicyVersion: "reference-v1",
		Credentials: heimdall.Credentials{
			ClientSecret: func(context.Context) (string, error) { return c.ClientSecret, nil },
			OpenFGAKey:   func(context.Context) (string, error) { return c.OpenFGAKey, nil },
		},
	})
	if err != nil {
		var failure *heimdall.Error
		if errors.As(err, &failure) {
			t.Log("SCIM_STEP runtime-" + failure.Code)
		}
		t.Fatal("runtime initialization failed")
	}
	fmt.Println("SCIM_STEP operator")
	actor, err := client.Tokens().Verify(ctx, c.OperatorToken)
	if err != nil {
		t.Fatal(err)
	}
	identity := heimdall.Identity{Issuer: c.Issuer, Subject: c.Subject}
	checkDenial := func(want bool) {
		t.Helper()
		if denied, err := storage.Identity.Check(ctx, identity); denied != want || err != nil {
			t.Fatal("unexpected durable denial", denied, err)
		}
	}
	checkDenial(true)
	if strings.HasPrefix(c.Stage, "cleanup") {
		fmt.Println("SCIM_STEP cleanup")
		cleanup, err := NewDirectoryCleanup(db, product, environment, c.Issuer, c.OrganizationID, "production")
		if err != nil {
			t.Fatal(err)
		}
		pending, err := cleanup.Pending(ctx, "", 2)
		if err != nil {
			t.Fatal("cannot read pending cleanup", err)
		}
		revision := c.Revision
		if c.Stage == "cleanup-replay" {
			if len(pending) != 0 {
				t.Fatal("acknowledged revision returned to pending")
			}
		} else if len(pending) == 1 && pending[0].Subject == c.Subject && pending[0].Reason == "inactive" {
			revision = pending[0].Revision
		} else {
			t.Fatal("expected the subject's cleanup revision", err)
		}
		result, err := cleanup.Reconcile(ctx, client, actor, c.Subject, revision, []string{c.WorkspaceID})
		if c.Stage == "cleanup-blocked" {
			if !errors.Is(err, heimdall.ErrUnavailable) || result != nil {
				t.Fatal("expected injected cleanup outage", result, err)
			}
			retry, err := cleanup.Pending(ctx, "", 2)
			if err != nil || len(retry) != 1 || retry[0] != pending[0] {
				t.Fatal("failed cleanup must leave the same revision pending", err)
			}
			checkDenial(true)
			fmt.Println("SCIM_REVISION " + revision)
			return
		}
		if err != nil || result != nil {
			t.Fatal("expected confirmed cleanup", result, err)
		}
		if pending, err = cleanup.Pending(ctx, "", 2); len(pending) != 0 || err != nil {
			t.Fatal("confirmed revision still pending", err)
		}
		fmt.Println("SCIM_REVISION " + revision)
		return
	}
	fmt.Println("SCIM_STEP prepare")
	prepare, err := NewDirectoryReactivation(db, product, environment, c.DirectoryReaderOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(ctx, c.Subject, c.Revision); err != nil {
		t.Fatal(err)
	}
	release, err := NewDirectoryReactivationRelease(db, product, environment, c.DirectoryReaderOptions)
	if err != nil {
		t.Fatal(err)
	}
	const ruleID = "browser-scim-v1"
	approval := []DirectoryReactivationEnrollment{{WorkspaceID: c.WorkspaceID, RuleID: ruleID}}
	if err := release(ctx, c.Subject, c.Revision, approval); !errors.Is(err, heimdall.ErrConflict) {
		t.Fatal("expected release to reject unconfirmed enrollment", err)
	}
	fmt.Println("SCIM_STEP enroll")
	enroll, err := NewDirectoryEnrollment(db, product, environment, c.DirectoryReaderOptions)
	if err != nil {
		t.Fatal(err)
	}
	result, err := enroll(ctx, client, actor, DirectoryEnrollmentInput{
		Subject: c.Subject, WorkspaceID: c.WorkspaceID, RuleID: ruleID,
		Roles: []string{c.ApprovedRole}, ReactivationRevision: c.Revision,
	})
	if err != nil || result.Status != "confirmed" {
		t.Fatal("expected confirmed enrollment", result, err)
	}
	checkDenial(true)
	fmt.Println("SCIM_STEP release")
	if err := release(ctx, c.Subject, c.Revision, approval); err != nil {
		t.Fatal(err)
	}
	checkDenial(false)
}
