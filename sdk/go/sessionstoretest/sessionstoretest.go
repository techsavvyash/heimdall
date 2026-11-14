// Package sessionstoretest verifies the public session store contract.
package sessionstoretest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
)

// Verify runs against two clients of the same disposable test backend.
func Verify(t *testing.T, first, second heimdall.SessionStore) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	key := "heimdall.v1|conformance|test|session|" + hex.EncodeToString(randomKey(t))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := first.Delete(ctx, key); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	value, next := []byte{0, 42, 255}, []byte{255, 42, 0}
	revisions := map[string]bool{}
	revision := func(rev string) string {
		n, err := strconv.ParseInt(rev, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != rev || revisions[rev] {
			t.Fatal("invalid or repeated revision")
		}
		revisions[rev] = true
		return rev
	}
	expires := time.Now().Add(time.Minute).UnixMilli()
	if r, err := first.Read(ctx, key); err != nil || r != nil {
		t.Fatal("missing record", err)
	}
	if r, err := first.CompareAndSwap(ctx, key, "1", value, expires); err != nil || r != "" {
		t.Fatal("missing CAS", err)
	}
	initial := revision(singleWinner(t, first, second, func(store heimdall.SessionStore) (string, error) {
		return store.Create(ctx, key, value, expires)
	}))
	checkRead(t, ctx, second, key, initial, value, expires)
	updated := revision(singleWinner(t, first, second, func(store heimdall.SessionStore) (string, error) {
		return store.CompareAndSwap(ctx, key, initial, next, expires)
	}))
	checkRead(t, ctx, first, key, updated, next, expires)
	for _, store := range []heimdall.SessionStore{first, second} {
		if err := store.Delete(ctx, key); err != nil {
			t.Fatal("delete", err)
		}
	}
	if r, err := first.CompareAndSwap(ctx, key, updated, next, expires); err != nil || r != "" {
		t.Fatal("CAS after delete", err)
	}
	shortExpiry := time.Now().Add(time.Second).UnixMilli()
	r, err := first.Create(ctx, key, value, shortExpiry)
	if err != nil {
		t.Fatal("short-lived create", err)
	}
	shortRevision := revision(r)
	timer := time.NewTimer(time.Until(time.UnixMilli(shortExpiry)) + 25*time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	if r, err := second.Read(ctx, key); err != nil || r != nil {
		t.Fatal("expired read", err)
	}
	if r, err := second.CompareAndSwap(ctx, key, shortRevision, next, expires); err != nil || r != "" {
		t.Fatal("expired CAS", err)
	}
	r, err = second.Create(ctx, key, next, expires)
	if err != nil {
		t.Fatal("create after expiry", err)
	}
	revision(r)
}

func randomKey(t *testing.T) []byte {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func checkRead(t *testing.T, ctx context.Context, store heimdall.SessionStore, key, revision string, value []byte, expires int64) {
	t.Helper()
	record, err := store.Read(ctx, key)
	if err != nil || record == nil || record.Revision != revision || record.ExpiresAt != expires || !bytes.Equal(record.Value, value) {
		t.Fatal("read does not match committed record", err)
	}
}

func singleWinner(t *testing.T, first, second heimdall.SessionStore, write func(heimdall.SessionStore) (string, error)) string {
	t.Helper()
	type result struct {
		revision string
		err      error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, store := range []heimdall.SessionStore{first, second} {
		go func() {
			<-start
			r, err := write(store)
			results <- result{r, err}
		}()
	}
	close(start)
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || (a.revision == "") == (b.revision == "") {
		t.Fatal("expected one committed write", a.err, b.err)
	}
	return a.revision + b.revision
}
