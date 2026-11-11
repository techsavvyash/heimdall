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

func TestKeyRotationPhases(t *testing.T) {
	url := os.Getenv("HEIMDALL_ROTATION_PRODUCT")
	if url == "" {
		t.Skip("live fleet rotation")
	}
	app, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.SetMaxOpenConns(1)
	ledger, err := sql.Open("postgres", os.Getenv("HEIMDALL_ROTATION_LEDGER"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	ledger.SetMaxOpenConns(1)
	raw, err := os.ReadFile("../../../../contracts/fixtures/key-rotation.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		ExpiresAt int64
		Payload   string
		Keys      map[string]byte
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	keys := map[string][]byte{}
	for id, value := range f.Keys {
		keys[id] = bytes.Repeat([]byte{value}, 32)
	}
	records := func(active string) *Records {
		cipher, err := contract.NewRecordCipher(active, keys)
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewRecords(app, ledger, cipher, "support", "production", nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	ctx := context.Background()
	value := []byte(f.Payload)
	create := func(active, kind, key string) {
		t.Helper()
		if _, err := records(active).Create(ctx, kind, key, value, f.ExpiresAt); err != nil {
			t.Fatal(err)
		}
	}
	read := func(active, kind, key string, present bool) {
		t.Helper()
		r, err := records(active).Get(ctx, kind, key)
		if err != nil || (r != nil) != present {
			t.Fatal(key, err)
		}
		if present && !bytes.Equal(r.Value, value) {
			t.Fatal("changed payload")
		}
	}
	refuse := func(active, key string) {
		t.Helper()
		if _, err := records(active).Create(ctx, "login", key, value, f.ExpiresAt); err != contract.ErrUnavailable {
			t.Fatal(err)
		}
	}
	switch os.Getenv("HEIMDALL_ROTATION_PHASE") {
	case "initial":
		create("old", "session", "snapshot")
	case "rolling":
		read("next", "session", "snapshot", true)
		create("next", "login", "rotated")
		create("old", "login", "lagging")
		if err := records("old").Delete(ctx, "session", "snapshot"); err != nil {
			t.Fatal(err)
		}
	case "retired":
		refuse("old", "retired")
		read("next", "login", "lagging", true)
		read("next", "session", "snapshot", false)
		create("next", "login", "after-retirement")
	case "recovered":
		read("recovery", "session", "snapshot", false)
		read("recovery", "login", "rotated", false)
		refuse("old", "refused-old")
		refuse("next", "refused-next")
		create("recovery", "session", "fresh")
		read("recovery", "session", "fresh", true)
	default:
		t.Fatal("unknown rotation phase")
	}
}
