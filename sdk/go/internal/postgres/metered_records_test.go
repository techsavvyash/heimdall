package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"os"
	"testing"
)

func TestMeteredRecords(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_KEYS_ADMIN")
	if url == "" {
		t.Skip("live metered records")
	}
	app, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.SetMaxOpenConns(1)
	usage, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_KEYS_RUNTIME"))
	if err != nil {
		t.Fatal(err)
	}
	defer usage.Close()
	usage.SetMaxOpenConns(1)
	ctx := context.Background()
	raw, err := os.ReadFile("../../../../contracts/fixtures/metered-records.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		InitialLimit, RotationLimit, ExpiresAt int64
		Payload                                string
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	expires := fixture.ExpiresAt
	value := []byte(fixture.Payload)
	cipher, err := contract.NewRecordCipher("old", map[string][]byte{"old": bytes.Repeat([]byte{12}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	if err = RegisterRecordKeyUsage(ctx, app, cipher.ActiveFingerprint(), fixture.InitialLimit, true); err != nil {
		t.Fatal(err)
	}
	records, err := NewRecords(app, usage, cipher, "metered-go", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionRecords(app, usage, cipher, "metered-go", "test", func([]byte) (SessionPrincipal, error) {
		return SessionPrincipal{"metered-go", "test", "https://issuer.example", "user"}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	login, err := records.Create(ctx, "login", "login", value, expires)
	if err != nil {
		t.Fatal(err)
	}
	if row, err := records.CompareAndSwap(ctx, "login", "login", "9223372036854775807", value, expires); err != nil || row != nil {
		t.Fatal(row, err)
	}
	session, err := sessions.CreateSession(ctx, "session", value, expires)
	if err != nil {
		t.Fatal(err)
	}
	if row, err := sessions.CompareAndSwapSession(ctx, "session", session.Revision, value, expires); err != nil || row == nil {
		t.Fatal(err)
	}
	if _, err = records.Create(ctx, "login", "login", value, expires); err != contract.ErrConflict {
		t.Fatal(err)
	}
	if _, err = sessions.InvalidateIdentity(ctx, "https://issuer.example", "user", 1, "inactive"); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.CreateSession(ctx, "denied", value, expires); err != contract.ErrUnauthenticated {
		t.Fatal(err)
	}
	for _, call := range []func() (*Record, error){func() (*Record, error) { return records.Create(ctx, "login", "over", value, expires) }, func() (*Record, error) {
		return records.CompareAndSwap(ctx, "login", "login", login.Revision, value, expires)
	}, func() (*Record, error) { return sessions.CreateSession(ctx, "over", value, expires) }, func() (*Record, error) {
		return sessions.CompareAndSwapSession(ctx, "session", session.Revision, value, expires)
	}} {
		if _, err = call(); err != contract.ErrUnavailable {
			t.Fatal(err)
		}
	}
	alias, _ := contract.NewRecordCipher("renamed", map[string][]byte{"renamed": bytes.Repeat([]byte{12}, 32)})
	if alias.ActiveFingerprint() != cipher.ActiveFingerprint() {
		t.Fatal("alias reset")
	}
	other, _ := NewRecords(app, usage, alias, "other", "test", nil)
	if _, err = other.Create(ctx, "login", "alias", value, expires); err != contract.ErrUnavailable {
		t.Fatal(err)
	}
	if err = RetireRecordKeyUsage(ctx, app, cipher.ActiveFingerprint()); err != nil {
		t.Fatal(err)
	}
	rotated, _ := contract.NewRecordCipher("next", map[string][]byte{"old": bytes.Repeat([]byte{12}, 32), "next": bytes.Repeat([]byte{15}, 32)})
	if err = RegisterRecordKeyUsage(ctx, app, rotated.ActiveFingerprint(), fixture.RotationLimit, true); err != nil {
		t.Fatal(err)
	}
	replacement, _ := NewRecords(app, usage, rotated, "metered-go", "test", nil)
	if row, err := replacement.Get(ctx, "login", "login"); err != nil || row == nil || !bytes.Equal(row.Value, value) {
		t.Fatal(err)
	}
	if row, err := replacement.CompareAndSwap(ctx, "login", "login", login.Revision, value, expires); err != nil || row == nil {
		t.Fatal(err)
	}
	if _, err = replacement.Create(ctx, "login", "over", value, expires); err != contract.ErrUnavailable {
		t.Fatal(err)
	}
	var used int
	if err = app.QueryRow("SELECT used FROM heimdall_keys.record_key_usage WHERE fingerprint=decode($1,'hex')", cipher.ActiveFingerprint()).Scan(&used); err != nil || int64(used) != fixture.InitialLimit {
		t.Fatal(used, err)
	}
}
func TestMeteredRecordsLoss(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_KEYS_LOSS")
	if url == "" {
		t.Skip("live metered commit loss")
	}
	app, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_KEYS_ADMIN"))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	usage, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer usage.Close()
	ctx := context.Background()
	cipher, _ := contract.NewRecordCipher("loss", map[string][]byte{"loss": bytes.Repeat([]byte{14}, 32)})
	if err = RegisterRecordKeyUsage(ctx, app, cipher.ActiveFingerprint(), 10, true); err != nil {
		t.Fatal(err)
	}
	records, _ := NewRecords(app, usage, cipher, "metered-loss-go", "test", nil)
	if _, err = records.Create(ctx, "login", "loss", []byte("payload"), 4102444800000); err != contract.ErrOutcomeUnknown {
		t.Fatal(err)
	}
	if row, err := records.Get(ctx, "login", "loss"); err != nil || row != nil {
		t.Fatal(row, err)
	}
}

func TestMeteredRecordsCancellation(t *testing.T) {
	db, err := sql.Open("postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, _ := contract.NewRecordCipher("test", map[string][]byte{"test": bytes.Repeat([]byte{11}, 32)})
	records, err := NewRecords(db, db, cipher, "ledger", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = records.Create(ctx, "login", "cancelled", []byte("payload"), 4102444800000); err != contract.ErrCancelled {
		t.Fatal(err)
	}
	if db.Stats().OpenConnections != 0 {
		t.Fatal("opened connection after cancellation")
	}
}
