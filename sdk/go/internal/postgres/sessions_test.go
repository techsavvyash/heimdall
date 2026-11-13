package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type sessionFixture struct {
	Record             json.RawMessage
	IdentityHash, Lock string
	Now, ExpiresAt     int64
}

func sessionSetup(t *testing.T, db *sql.DB, product string) (*SessionRecords, sessionFixture) {
	t.Helper()
	raw, err := os.ReadFile("../../../../contracts/fixtures/session-storage.json")
	if err != nil {
		t.Fatal(err)
	}
	var f sessionFixture
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"enterprise-evidence", "session-payload"} {
		raw, err := os.ReadFile("../../../../contracts/" + name + ".schema.json")
		if err != nil {
			t.Fatal(err)
		}
		var schema any
		if err = json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if err = compiler.AddResource(schema.(map[string]any)["$id"].(string), schema); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ = os.ReadFile("../../../../contracts/session-payload.schema.json")
	var root map[string]any
	_ = json.Unmarshal(raw, &root)
	shape, err := compiler.Compile(root["$id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	validate := func(raw []byte) (SessionPrincipal, error) {
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil || shape.Validate(v) != nil {
			return SessionPrincipal{}, contract.ErrInvalidArgument
		}
		return SessionPrincipal{v["productId"].(string), v["environment"].(string), v["issuer"].(string), v["subject"].(string)}, nil
	}
	cipher, err := contract.NewRecordCipher("test-key", map[string][]byte{"test-key": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSessionRecords(db, db, cipher, product, "production", validate, func() int64 { return f.Now })
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}
func sessionPayload(f sessionFixture, product, subject string) []byte {
	var v map[string]any
	_ = json.Unmarshal(f.Record, &v)
	v["productId"] = product
	if subject != "" {
		v["subject"] = subject
	}
	raw, _ := json.Marshal(v)
	return raw
}
func TestSessionReplacement(t *testing.T) {
	dsn := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live PostgreSQL integration")
	}
	db, err := sql.Open("postgres", dsn+"&options=-c%20role%3Dheimdall_identity_runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, f := sessionSetup(t, db, "switch-go")
	ctx := context.Background()
	oldValue := sessionPayload(f, "switch-go", "")
	newValue := sessionPayload(f, "switch-go", "new-account")
	old, err := s.CreateSession(ctx, "previous", oldValue, f.ExpiresAt)
	if err != nil || old == nil {
		t.Fatal("create previous session", err)
	}
	if _, err = s.InvalidateIdentity(ctx, "https://id.example.com", "disabled-account", f.Now, "inactive"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReplaceSession(ctx, "previous", old.Revision, "disabled", sessionPayload(f, "switch-go", "disabled-account"), f.ExpiresAt); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("disabled account replaced previous session", err)
	}
	refreshed, err := s.CompareAndSwapSession(ctx, "previous", old.Revision, oldValue, f.ExpiresAt)
	if err != nil || refreshed == nil {
		t.Fatal("previous session lost", err)
	}
	if result, err := s.ReplaceSession(ctx, "previous", old.Revision, "stale", newValue, f.ExpiresAt); err != nil || result != nil {
		t.Fatal("stale switch applied", err)
	}
	results := make(chan *Record, 2)
	for _, key := range []string{"next-a", "next-b"} {
		go func() {
			result, err := s.ReplaceSession(ctx, "previous", refreshed.Revision, key, newValue, f.ExpiresAt)
			if err != nil {
				t.Error(err)
			}
			results <- result
		}()
	}
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatal("expected one replacement")
	}
	if result, err := s.GetSession(ctx, "previous"); err != nil || result != nil {
		t.Fatal("previous key still authenticates", err)
	}
	if result, err := s.CompareAndSwapSession(ctx, "previous", refreshed.Revision, oldValue, f.ExpiresAt); err != nil || result != nil {
		t.Fatal("old activity restored the replaced session", err)
	}
	winner := "next-a"
	if a == nil {
		winner = "next-b"
	}
	result, err := s.GetSession(ctx, winner)
	if err != nil || result == nil || !bytes.Equal(result.Value, newValue) {
		t.Fatal("replacement payload was lost", err)
	}
}

func TestSessionRecords(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("live sessions")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtime, err := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_identity_runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	shared, f := sessionSetup(t, runtime, "ledger-identity")
	ctx := context.Background()
	identity, err := identityBinding("https://id.example.com", "Maya\x00東京")
	if err != nil || hex.EncodeToString(identity) != f.IdentityHash || strconv.FormatInt(identityLock("ledger-identity", "production", identity), 10) != f.Lock {
		t.Fatal("identity/lock parity", err)
	}
	peer, err := shared.GetSession(ctx, "from-ts-session")
	if err != nil || peer == nil {
		t.Fatal("TS peer", err)
	}
	var got, want any
	_ = json.Unmarshal(peer.Value, &got)
	_ = json.Unmarshal(f.Record, &want)
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if !bytes.Equal(a, b) {
		t.Fatal("peer payload")
	}
	if os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		return
	}
	if _, err = shared.CreateSession(ctx, "from-go-session", f.Record, f.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if n, e := shared.InvalidateIdentity(ctx, "https://id.example.com", "cross-deny", 4000, "inactive"); e != nil || n != 1 {
		t.Fatal("cross invalidation", n, e)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("SELECT pg_advisory_xact_lock($1::bigint)", f.Lock); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err = shared.CreateSession(short, "go-blocked", f.Record, f.ExpiresAt)
	cancel()
	_ = tx.Rollback()
	if !errors.Is(err, contract.ErrDeadlineExceeded) {
		t.Fatal("shared lock", err)
	}
	s, _ := sessionSetup(t, runtime, "identity-go")
	value := sessionPayload(f, "identity-go", "")
	if denied, e := s.IsIdentityDenied(ctx, "https://id.example.com", "Maya\x00東京"); e != nil || denied {
		t.Fatal("initial deny gate", e)
	}
	if _, err = s.CreateSession(ctx, "invalid", []byte("{}"), f.ExpiresAt); !errors.Is(err, contract.ErrInvalidArgument) {
		t.Fatal(err)
	}
	first, err := s.CreateSession(ctx, "go-one", value, f.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateSession(ctx, "go-two", value, f.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if r, e := s.records.Get(ctx, "session", "go-one"); e != nil || r != nil {
		t.Fatal("generic bypass", e)
	}
	if r, e := s.records.CompareAndSwap(ctx, "session", "go-one", first.Revision, value, f.ExpiresAt); e != nil || r != nil {
		t.Fatal("generic CAS bypass", e)
	}
	if err = s.records.Delete(ctx, "session", "go-one"); err != nil {
		t.Fatal(err)
	}
	if r, e := s.CompareAndSwapSession(ctx, "go-one", first.Revision, sessionPayload(f, "identity-go", "different"), f.ExpiresAt); e != nil || r != nil {
		t.Fatal("identity switch", e)
	}
	if r, e := s.CompareAndSwapSession(ctx, "go-one", first.Revision, value, f.ExpiresAt); e != nil || r == nil {
		t.Fatal("CAS", e)
	}
	if _, err = s.CreateSession(ctx, "go-other", sessionPayload(f, "identity-go", "other"), f.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if n, e := s.InvalidateIdentity(ctx, "https://id.example.com", "Maya\x00東京", 5000, "inactive"); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	if denied, e := s.IsIdentityDenied(ctx, "https://id.example.com", "Maya\x00東京"); e != nil || !denied {
		t.Fatal("persisted deny gate", e)
	}
	if denied, e := s.IsIdentityDenied(ctx, "https://id.example.com", "other"); e != nil || denied {
		t.Fatal("other identity gate", e)
	}
	if r, e := s.GetSession(ctx, "go-one"); e != nil || r != nil {
		t.Fatal("denied read", e)
	}
	if r, e := s.GetSession(ctx, "go-other"); e != nil || r == nil {
		t.Fatal("other identity", e)
	}
	if _, e := s.CreateSession(ctx, "go-denied", value, f.ExpiresAt); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal(e)
	}
	_, err = s.InvalidateIdentity(ctx, "https://id.example.com", "Maya\x00東京", 4000, "deleted")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.InvalidateIdentity(ctx, "https://id.example.com", "Maya\x00東京", 6000, "inactive")
	if err != nil {
		t.Fatal(err)
	}
	var observed int64
	var reason string
	if err = db.QueryRow("SELECT observed_at,reason FROM heimdall.identity_denials WHERE product_id='identity-go' AND identity_hash=$1", identity).Scan(&observed, &reason); err != nil || observed != 6000 || reason != "deleted" {
		t.Fatal(observed, reason, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 9)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.CreateSession(ctx, fmt.Sprintf("go-race-%d", i), sessionPayload(f, "identity-go", "racing"), f.ExpiresAt)
			errs <- e
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, e := s.InvalidateIdentity(ctx, "https://id.example.com", "racing", 5000, "inactive")
		errs <- e
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil && !errors.Is(e, contract.ErrUnauthenticated) {
			t.Fatal(e)
		}
	}
	for i := range 8 {
		if r, e := s.GetSession(ctx, fmt.Sprintf("go-race-%d", i)); e != nil || r != nil {
			t.Fatal("race survivor", e)
		}
	}
}

func TestIdentityCommitLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_IDENTITY_COMMIT_LOSS")
	if url == "" {
		t.Skip("live COMMIT fault")
	}
	db, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	faulty, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer faulty.Close()
	healthy, f := sessionSetup(t, db, "identity-fault-go")
	fault, _ := sessionSetup(t, faulty, "identity-fault-go")
	ctx := context.Background()
	value := sessionPayload(f, "identity-fault-go", "")
	if _, err = healthy.CreateSession(ctx, "fault-session", value, f.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	gate, err := fault.IdentityGate("identity-fault-go", "production")
	if err != nil {
		t.Fatal(err)
	}
	if err = gate.Invalidate(ctx, identity.Negative{Identity: identity.Identity{Issuer: "https://id.example.com", Subject: "Maya\x00東京"}, Context: "bearer", TokenDigest: strings.Repeat("a", 64), Reason: "identity_inactive"}); !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	var denials, sessions int
	if err = db.QueryRow("SELECT (SELECT count(*) FROM heimdall.identity_denials WHERE product_id='identity-fault-go'),(SELECT count(*) FROM heimdall.records WHERE product_id='identity-fault-go')").Scan(&denials, &sessions); err != nil || denials != 1 || sessions != 0 {
		t.Fatal(denials, sessions, err)
	}
	if n, e := healthy.InvalidateIdentity(ctx, "https://id.example.com", "Maya\x00東京", 4000, "inactive"); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if _, err = healthy.CreateSession(ctx, "retry-session", value, f.ExpiresAt); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal(err)
	}
}

type conditionalFixture struct {
	InvalidRevisions []string
	RaceCount        int
	SharedKey        string
}

func conditionalCases(t *testing.T) conditionalFixture {
	t.Helper()
	raw, e := os.ReadFile("../../../../contracts/fixtures/session-invalidation.json")
	if e != nil {
		t.Fatal(e)
	}
	var f conditionalFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("conditional fixture")
	}
	return f
}
func TestConditionalRevisionValidation(t *testing.T) {
	s := &IdentityRecords{}
	for _, revision := range conditionalCases(t).InvalidRevisions {
		if _, e := (&SessionRecords{}).CompareAndSwapSession(context.Background(), "key", revision, nil, 0); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(revision, e)
		}
		if _, e := s.InvalidateSessionVersion(context.Background(), "key", revision, "https://id.example.com", "subject"); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(revision, e)
		}
	}
}
func TestConditionalSessionInvalidation(t *testing.T) {
	if os.Getenv("HEIMDALL_TEST_DATABASE_URL") == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("live writer")
	}
	db, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL")+"&options=-c%20role%3Dheimdall_identity_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, f := sessionSetup(t, db, "conditional-go")
	c := conditionalCases(t)
	ctx := context.Background()
	issuer, subject := "https://id.example.com", "Maya\x00東京"
	value := sessionPayload(f, "conditional-go", "")
	first, e := s.CreateSession(ctx, "one", value, f.ExpiresAt)
	if e != nil {
		t.Fatal(e)
	}
	for _, identity := range [][2]string{{issuer, "other"}, {"https://other.example.com", subject}} {
		if removed, e := s.InvalidateSessionVersion(ctx, "one", first.Revision, identity[0], identity[1]); e != nil || removed {
			t.Fatal("identity isolation", e)
		}
	}
	other, _ := sessionSetup(t, db, "conditional-other-go")
	stage, e := NewSessionRecords(db, db, s.records.cipher, "conditional-go", "staging", s.validate, func() int64 { return f.Now })
	if e != nil {
		t.Fatal(e)
	}
	for _, store := range []*SessionRecords{other, stage} {
		if removed, e := store.InvalidateSessionVersion(ctx, "one", first.Revision, issuer, subject); e != nil || removed {
			t.Fatal("scope isolation", e)
		}
	}
	newer, e := s.CompareAndSwapSession(ctx, "one", first.Revision, value, f.ExpiresAt)
	if e != nil || newer == nil {
		t.Fatal(e)
	}
	if removed, e := s.InvalidateSessionVersion(ctx, "one", first.Revision, issuer, subject); e != nil || removed {
		t.Fatal("stale revision", e)
	}
	if current, e := s.GetSession(ctx, "one"); e != nil || current == nil || current.Revision != newer.Revision {
		t.Fatal("replacement removed", e)
	}
	if removed, e := s.InvalidateSessionVersion(ctx, "one", newer.Revision, issuer, subject); e != nil || !removed {
		t.Fatal("matching delete", e)
	}
	if removed, e := s.InvalidateSessionVersion(ctx, "one", newer.Revision, issuer, subject); e != nil || removed {
		t.Fatal("repeat delete", e)
	}
	if changed, e := s.CompareAndSwapSession(ctx, "one", newer.Revision, value, f.ExpiresAt); e != nil || changed != nil {
		t.Fatal("CAS resurrected delete", e)
	}
	replacement, e := s.CreateSession(ctx, "one", value, f.ExpiresAt)
	if e != nil {
		t.Fatal(e)
	}
	if removed, e := s.InvalidateSessionVersion(ctx, "one", newer.Revision, issuer, subject); e != nil || removed {
		t.Fatal("reused revision", e)
	}
	if current, e := s.GetSession(ctx, "one"); e != nil || current == nil || current.Revision != replacement.Revision {
		t.Fatal("recreated session removed", e)
	}
	for i := range c.RaceCount {
		key := fmt.Sprintf("conditional-race-%d", i)
		record, e := s.CreateSession(ctx, key, value, f.ExpiresAt)
		if e != nil {
			t.Fatal(e)
		}
		type result struct {
			changed *Record
			removed bool
			err     error
		}
		results := make(chan result, 2)
		go func() {
			r, e := s.CompareAndSwapSession(ctx, key, record.Revision, value, f.ExpiresAt)
			results <- result{changed: r, err: e}
		}()
		go func() {
			r, e := s.InvalidateSessionVersion(ctx, key, record.Revision, issuer, subject)
			results <- result{removed: r, err: e}
		}()
		winners := 0
		var winner *Record
		for range 2 {
			r := <-results
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.changed != nil {
				winner = r.changed
				winners++
			}
			if r.removed {
				winners++
			}
		}
		if winners != 1 {
			t.Fatal("race winner count", winners)
		}
		current, e := s.GetSession(ctx, key)
		if e != nil {
			t.Fatal(e)
		}
		if winner == nil {
			if current != nil {
				t.Fatal("deleted record survived")
			}
		} else if current == nil || current.Revision != winner.Revision {
			t.Fatal("CAS winner removed")
		}
	}
	shared, _ := sessionSetup(t, db, "ledger-identity")
	record, e := shared.GetSession(ctx, c.SharedKey)
	if e != nil || record == nil {
		t.Fatal("TS peer record", e)
	}
	if removed, e := shared.InvalidateSessionVersion(ctx, c.SharedKey, record.Revision, issuer, subject); e != nil || !removed {
		t.Fatal("TS peer deletion", e)
	}
}
func TestConditionalSessionCommitLoss(t *testing.T) {
	faultURL := os.Getenv("HEIMDALL_TEST_SESSION_DELETE_LOSS")
	if faultURL == "" {
		t.Skip("live COMMIT fault")
	}
	db, e := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	fault, e := sql.Open("postgres", faultURL)
	if e != nil {
		t.Fatal(e)
	}
	defer fault.Close()
	s, f := sessionSetup(t, db, "conditional-fault-go")
	broken, _ := sessionSetup(t, fault, "conditional-fault-go")
	ctx := context.Background()
	issuer, subject := "https://id.example.com", "Maya\x00東京"
	value := sessionPayload(f, "conditional-fault-go", "")
	record, e := s.CreateSession(ctx, "fault", value, f.ExpiresAt)
	if e != nil {
		t.Fatal(e)
	}
	gate, e := broken.IdentityGate("conditional-fault-go", "production")
	if e != nil {
		t.Fatal(e)
	}
	contextKey, e := identity.SessionIdentityContext("fault", record.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if e = gate.Invalidate(ctx, identity.Negative{Identity: identity.Identity{Issuer: issuer, Subject: subject}, Context: contextKey, TokenDigest: strings.Repeat("a", 64), Reason: "token_inactive"}); !errors.Is(e, contract.ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	if current, e := s.GetSession(ctx, "fault"); e != nil || current != nil {
		t.Fatal("confirmed commit not persisted", e)
	}
	replacement, e := s.CreateSession(ctx, "fault", value, f.ExpiresAt)
	if e != nil {
		t.Fatal(e)
	}
	if removed, e := s.InvalidateSessionVersion(ctx, "fault", record.Revision, issuer, subject); e != nil || removed {
		t.Fatal("retry removed replacement", e)
	}
	if current, e := s.GetSession(ctx, "fault"); e != nil || current == nil || current.Revision != replacement.Revision {
		t.Fatal("replacement absent", e)
	}
}

type gateTransport func(*http.Request) (*http.Response, error)

func (f gateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestIdentityGate(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_DATABASE_URL")
	if url == "" || os.Getenv("HEIMDALL_TEST_READ_PEER") != "" {
		t.Skip("durable identity gate")
	}
	db, e := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_identity_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	sessions, f := sessionSetup(t, db, "gate-go")
	if _, e = sessions.IdentityGate("support", "production"); e != contract.ErrInvalidArgument {
		t.Fatal("cross-product gate")
	}
	if _, e = sessions.IdentityGate("gate-go", "staging"); e != contract.ErrInvalidArgument {
		t.Fatal("cross-environment gate")
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/token-verification.json")
	if e != nil {
		t.Fatal(e)
	}
	var token struct {
		Issuer, Audience, Subject string
		Now                       int64
		JWKS                      json.RawMessage
		Vectors                   []struct{ Name, Token string }
	}
	if e = json.Unmarshal(raw, &token); e != nil {
		t.Fatal(e)
	}
	var access string
	for _, v := range token.Vectors {
		if v.Name == "valid" {
			access = v.Token
		}
	}
	raw, e = os.ReadFile("../../../../contracts/fixtures/identity-status.json")
	if e != nil {
		t.Fatal(e)
	}
	type status struct {
		Name, NegativeReason            string
		Introspection, User             json.RawMessage
		IntrospectionStatus, UserStatus int
	}
	var cases []status
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	active := cases[0]
	var inactive, disabled status
	for _, c := range cases {
		if c.Name == "inactive-token" {
			inactive = c
		}
		if c.NegativeReason == "identity_inactive" {
			disabled = c
		}
	}
	var mu sync.Mutex
	current := active
	online := 0
	var replace func()
	original := http.DefaultTransport
	http.DefaultTransport = gateTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		body := token.JWKS
		code := 200
		if strings.HasSuffix(r.URL.Path, "/introspect") {
			online++
			if replace != nil {
				replace()
				replace = nil
			}
			body = current.Introspection
			code = current.IntrospectionStatus
		} else if r.URL.Path != "/keys" {
			body = current.User
			code = current.UserStatus
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
	})
	defer func() { http.DefaultTransport = original }()
	sessions.records.now = func() int64 { return token.Now }
	identities, e := NewIdentityRecords(db, "gate-go", "production", func() int64 { return token.Now })
	if e != nil {
		t.Fatal(e)
	}
	gate, e := identities.IdentityGate("gate-go", "production")
	if e != nil {
		t.Fatal(e)
	}
	config := identity.StatusConfig{Config: identity.Config{Issuer: token.Issuer, Audience: token.Audience, JWKSURI: token.Issuer + "/keys", Profile: "production"}, ProductID: "gate-go", Environment: "production", PolicyVersion: "p1", ClientID: "client"}
	auditDB, e := sql.Open("postgres", url+"&options=-c%20role%3Dheimdall_audit_runtime")
	if e != nil {
		t.Fatal(e)
	}
	defer auditDB.Close()
	_, validateAudit := auditFixtureValidator(t)
	audit, e := NewAudit(auditDB, "gate-go", "production", []string{"siem"}, validateAudit)
	if e != nil {
		t.Fatal(e)
	}
	warm, e := auditflow.NewAuthentication(config, func(context.Context) (string, error) { return "secret", nil }, gate, audit.Append, func() time.Time { return time.UnixMilli(token.Now) })
	if e != nil {
		t.Fatal(e)
	}
	zero := int64(0)
	config.FreshnessMs = &zero
	fresh, e := auditflow.NewAuthentication(config, func(context.Context) (string, error) { return "secret", nil }, gate, audit.Append, func() time.Time { return time.UnixMilli(token.Now) })
	if e != nil {
		t.Fatal(e)
	}
	var record map[string]any
	_ = json.Unmarshal(f.Record, &record)
	record["productId"] = "gate-go"
	record["issuer"] = token.Issuer
	record["subject"] = token.Subject
	record["accessToken"] = access
	record["createdAt"] = token.Now - 1000
	record["lastActivityAt"] = token.Now
	record["identityValidatedAt"] = token.Now
	record["absoluteExpiresAt"] = token.Now + 28800000
	record["idleExpiresAt"] = token.Now + 1800000
	record["accessTokenExpiresAt"] = token.Now + 3600000
	value, _ := json.Marshal(record)
	expiry := token.Now + 1800000
	ctx := context.Background()
	first, e := sessions.CreateSession(ctx, "first", value, expiry)
	if e != nil {
		t.Fatal(e)
	}
	other, e := sessions.CreateSession(ctx, "other", value, expiry)
	if e != nil {
		t.Fatal(e)
	}
	contextKey, _ := identity.SessionIdentityContext("first", first.Revision)
	if p, e := warm.Verify(ctx, access, contextKey); e != nil || p.Identity().Subject != token.Subject {
		t.Fatal("active", e)
	}
	mu.Lock()
	current = inactive
	mu.Unlock()
	if _, e = fresh.Verify(ctx, access, "bearer"); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal("bearer negative", e)
	}
	if r, e := sessions.GetSession(ctx, "first"); e != nil || r == nil {
		t.Fatal("bearer removed session", e)
	}
	if _, e = fresh.Verify(ctx, access, contextKey); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal("negative", e)
	}
	if r, e := sessions.GetSession(ctx, "first"); e != nil || r != nil {
		t.Fatal("token session survived", e)
	}
	if denied, e := sessions.IsIdentityDenied(ctx, token.Issuer, token.Subject); e != nil || denied {
		t.Fatal("token banned identity", e)
	}
	mu.Lock()
	replace = func() {
		if _, e := sessions.CompareAndSwapSession(ctx, "other", other.Revision, value, expiry); e != nil {
			t.Error(e)
		}
	}
	mu.Unlock()
	otherContext, _ := identity.SessionIdentityContext("other", other.Revision)
	if _, e = fresh.Verify(ctx, access, otherContext); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal(e)
	}
	replacement, e := sessions.GetSession(ctx, "other")
	if e != nil || replacement == nil || replacement.Revision == other.Revision {
		t.Fatal("replacement lost", e)
	}
	record["subject"] = "unrelated"
	unrelated, _ := json.Marshal(record)
	if _, e = sessions.CreateSession(ctx, "unrelated", unrelated, expiry); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	current = disabled
	mu.Unlock()
	otherContext, _ = identity.SessionIdentityContext("other", replacement.Revision)
	if _, e = fresh.Verify(ctx, access, otherContext); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if denied, e := sessions.IsIdentityDenied(ctx, token.Issuer, token.Subject); e != nil || !denied {
		t.Fatal("identity not denied", e)
	}
	if r, e := sessions.GetSession(ctx, "other"); e != nil || r != nil {
		t.Fatal("identity session survived", e)
	}
	if r, e := sessions.GetSession(ctx, "unrelated"); e != nil || r == nil {
		t.Fatal("unrelated session removed", e)
	}
	if e = gate.Invalidate(ctx, identity.Negative{Identity: identity.Identity{Issuer: token.Issuer, Subject: token.Subject}, Context: "bearer", TokenDigest: strings.Repeat("a", 64), Reason: "identity_deleted"}); e != nil {
		t.Fatal(e)
	}
	var reason string
	if e = db.QueryRow("SELECT reason FROM heimdall.identity_denials WHERE product_id='gate-go'").Scan(&reason); e != nil || reason != "deleted" {
		t.Fatal("deleted mapping", e)
	}
	mu.Lock()
	before := online
	current = active
	mu.Unlock()
	if _, e = warm.Verify(ctx, access, contextKey); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal("warm cache bypassed local deny", e)
	}
	mu.Lock()
	after := online
	mu.Unlock()
	if after != before {
		t.Fatal("local deny consulted provider")
	}
	if _, e = sessions.CreateSession(ctx, "resurrect", value, expiry); !errors.Is(e, contract.ErrUnauthenticated) {
		t.Fatal("resurrected identity", e)
	}
	var eventCount, deliveryCount int
	if e = auditDB.QueryRow("SELECT count(*) FROM heimdall.audit_events WHERE product_id='gate-go' AND environment='production'").Scan(&eventCount); e != nil || eventCount != 6 {
		t.Fatal("authentication events", eventCount, e)
	}
	if e = auditDB.QueryRow("SELECT count(*) FROM heimdall.audit_deliveries WHERE product_id='gate-go' AND environment='production'").Scan(&deliveryCount); e != nil || deliveryCount != 6 {
		t.Fatal("authentication deliveries", deliveryCount, e)
	}

}
