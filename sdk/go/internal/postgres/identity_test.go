package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
)

func TestIdentityRecordsWithoutSessionCipher(t *testing.T) {
	// No connection is opened: token-only invalidation is deliberately a no-op.
	db, err := sql.Open("postgres", "postgres://unused")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, scope := range [][2]string{{"", "production"}, {"ledger\n", "production"}, {"ledger", "production\n"}} {
		if _, e := NewIdentityRecords(db, scope[0], scope[1], nil); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(scope, e)
		}
	}
	if _, e := NewIdentityRecords(nil, "ledger", "production", nil); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
	store, err := NewIdentityRecords(db, "ledger", "production", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range [][2]string{{"support", "production"}, {"ledger", "staging"}} {
		if _, e := store.IdentityGate(scope[0], scope[1]); !errors.Is(e, contract.ErrInvalidArgument) {
			t.Fatal(scope, e)
		}
	}
	gate, err := store.IdentityGate("ledger", "production")
	if err != nil {
		t.Fatal(err)
	}
	negative := identity.Negative{Identity: identity.Identity{Issuer: "https://id.example.com", Subject: "person"}, Context: "bearer", TokenDigest: strings.Repeat("a", 64), Reason: "token_inactive"}
	if e := gate.Invalidate(context.Background(), negative); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := gate.Invalidate(ctx, negative); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	negative.Context = "session:key:0"
	if e := gate.Invalidate(context.Background(), negative); !errors.Is(e, contract.ErrInvalidArgument) {
		t.Fatal(e)
	}
}
