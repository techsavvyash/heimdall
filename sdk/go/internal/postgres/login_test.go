package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type lostLoginWrite struct{ *Records }

func (s lostLoginWrite) CompareAndSwap(ctx context.Context, kind, key, revision string, value []byte, expires int64) (*Record, error) {
	updated, err := s.Records.CompareAndSwap(ctx, kind, key, revision, value, expires)
	if err == nil && updated != nil {
		return nil, contract.ErrOutcomeUnknown
	}
	return updated, err
}

func TestBrowserLoginRecords(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live PostgreSQL integration")
	}
	var fixture struct {
		Target     LoginTarget
		LifetimeMs int64
		Federation LoginFederation
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/browser-login-records.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	target := fixture.Target
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, err := contract.NewRecordCipher("k1", map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	clock := func() int64 { return now }
	store, err := NewRecords(db, db, cipher, target.ProductID, target.Environment, clock)
	if err != nil {
		t.Fatal(err)
	}
	login := NewBrowserLoginRecords(store, clock)
	ctx := context.Background()
	start := func() StartedLogin {
		t.Helper()
		started, err := login.Start(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		return started
	}
	started, second := start(), start()
	if started.ExpiresAt-now != fixture.LifetimeMs ||
		len(map[string]bool{started.State: true, started.BrowserBinding: true, started.Nonce: true, started.PKCEChallenge: true}) != 4 {
		t.Fatal("invalid start result")
	}
	changed := target
	changed.TenantPolicy = nil
	otherProduct := target
	otherProduct.ProductID = "support"
	if _, err := login.Start(ctx, otherProduct); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	for _, attempt := range []struct {
		target LoginTarget
		cookie string
	}{
		{target, second.BrowserBinding}, {changed, started.BrowserBinding}, {otherProduct, started.BrowserBinding},
	} {
		if _, err := login.Claim(ctx, attempt.target, started.State, attempt.cookie); !errors.Is(err, contract.ErrUnauthenticated) {
			t.Fatal(err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := login.Claim(cancelled, target, started.State, started.BrowserBinding); !errors.Is(err, contract.ErrCancelled) {
		t.Fatal(err)
	}
	claims := make(chan ClaimedLogin, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := login.Claim(ctx, target, started.State, started.BrowserBinding)
			if err == nil {
				claims <- claim
			} else if !errors.Is(err, contract.ErrUnauthenticated) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(claims)
	if len(claims) != 1 {
		t.Fatalf("expected one winner, got %d", len(claims))
	}
	claim := <-claims
	challenge := sha256.Sum256([]byte(claim.PKCEVerifier))
	if claim.Nonce != started.Nonce || base64.RawURLEncoding.EncodeToString(challenge[:]) != started.PKCEChallenge {
		t.Fatal("callback did not return original OIDC material")
	}
	const destination = "https://idp.example.test/authorize"
	federated, location, err := login.StartFederation(ctx, target, func(_, _, _ string, _ int64) (LoginFederation, string, error) {
		return fixture.Federation, destination, nil
	})
	if err != nil || location != destination {
		t.Fatal("federation start failed", err)
	}
	if _, err := login.Claim(ctx, target, federated.State, federated.BrowserBinding); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("ordinary callback claimed a federation record", err)
	}
	if _, err := login.ClaimFederation(ctx, target, federated.State, federated.BrowserBinding, "wrong_intent"); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("wrong intent claimed the record", err)
	}
	stale := target
	policy := *target.TenantPolicy
	policy.PolicyVersion = "v2"
	stale.TenantPolicy = &policy
	if _, err := login.ClaimFederation(ctx, stale, federated.State, federated.BrowserBinding, fixture.Federation.IntentID); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("changed policy claimed the record", err)
	}
	federatedClaims := make(chan ClaimedFederation, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := login.ClaimFederation(ctx, target, federated.State, federated.BrowserBinding, fixture.Federation.IntentID)
			if err == nil {
				federatedClaims <- claim
			} else if !errors.Is(err, contract.ErrUnauthenticated) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(federatedClaims) != 1 {
		t.Fatalf("expected one federation winner, got %d", len(federatedClaims))
	}
	if winner := <-federatedClaims; winner.AuthRequestID != fixture.Federation.AuthRequestID || winner.ExpiresAt != federated.ExpiresAt || winner.Nonce != federated.Nonce {
		t.Fatal("wrong federation binding")
	}
	if _, err := login.ClaimFederation(ctx, target, second.State, second.BrowserBinding, fixture.Federation.IntentID); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("federation callback claimed an ordinary record", err)
	}
	lost := start()
	unknown := NewBrowserLoginRecords(lostLoginWrite{store}, clock)
	if result, err := unknown.Claim(ctx, target, lost.State, lost.BrowserBinding); result != (ClaimedLogin{}) || !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal("unconfirmed write must withhold the exchange material", err)
	}
	if _, err := login.Claim(ctx, target, lost.State, lost.BrowserBinding); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal(err)
	}
	lostFederation, _, err := login.StartFederation(ctx, target, func(_, _, _ string, _ int64) (LoginFederation, string, error) {
		return fixture.Federation, destination, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := unknown.ClaimFederation(ctx, target, lostFederation.State, lostFederation.BrowserBinding, fixture.Federation.IntentID); result != (ClaimedFederation{}) || !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal("unconfirmed federation claim must withhold the exchange material", err)
	}
	if _, err := login.ClaimFederation(ctx, target, lostFederation.State, lostFederation.BrowserBinding, fixture.Federation.IntentID); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("consumed federation claim replayed", err)
	}
	malformed := start()
	stored, err := store.Get(ctx, "login", malformed.State)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	payload, err := decodeBrowserLogin(stored)
	if err != nil || payload.BrowserBindingDigest != target.scope(malformed.BrowserBinding).BrowserBindingDigest {
		t.Fatal("invalid stored browser digest", err)
	}
	payload.Nonce = ""
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompareAndSwap(ctx, "login", malformed.State, stored.Revision, value, stored.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := login.Claim(ctx, target, malformed.State, malformed.BrowserBinding); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal(err)
	}
	now = second.ExpiresAt
	if _, err := login.Claim(ctx, target, second.State, second.BrowserBinding); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal(err)
	}
}
