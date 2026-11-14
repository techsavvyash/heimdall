package browserlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
)

type memorySessionStore struct {
	mu       sync.Mutex
	rows     map[string]StoredSessionRecord
	sequence int
}

func (m *memorySessionStore) put(key string, value []byte, expires int64) string {
	m.sequence++
	revision := strconv.Itoa(m.sequence)
	m.rows[key] = StoredSessionRecord{Value: bytes.Clone(value), ExpiresAt: expires, Revision: revision}
	return revision
}

func (m *memorySessionStore) Create(_ context.Context, key string, value []byte, expires int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows[key].ExpiresAt > time.Now().UnixMilli() {
		return "", nil
	}
	return m.put(key, value, expires), nil
}

func (m *memorySessionStore) Read(_ context.Context, key string) (*StoredSessionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.rows[key]
	if record.ExpiresAt <= time.Now().UnixMilli() {
		return nil, nil
	}
	return &record, nil
}

func (m *memorySessionStore) CompareAndSwap(_ context.Context, key, revision string, value []byte, expires int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.rows[key]
	if record.Revision != revision || record.ExpiresAt <= time.Now().UnixMilli() {
		return "", nil
	}
	return m.put(key, value, expires), nil
}

func (m *memorySessionStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, key)
	return nil
}

func TestCustomSessionStore(t *testing.T) {
	var f struct {
		ProductID, Environment, AlternateEnvironment, RecordKey, LoginStorageKey, SessionStorageKey, Secret string
		InvalidRevisions                                                                                    []string
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/custom-session-store.json")
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("load fixture", err)
	}
	backend := &memorySessionStore{rows: make(map[string]StoredSessionRecord)}
	reservations := 0
	var mu sync.Mutex
	options := SessionStoreOptions{
		Store: backend, ActiveKeyID: "old",
		Keys:            map[string][]byte{"old": bytes.Repeat([]byte{7}, 32), "next": bytes.Repeat([]byte{8}, 32)},
		ReserveKeyUsage: func(context.Context, string) error { mu.Lock(); reservations++; mu.Unlock(); return nil },
	}
	first, err := NewCustomSessionStore(f.ProductID, f.Environment, options)
	if err != nil {
		t.Fatal(err)
	}
	options.ActiveKeyID = "next"
	second, err := NewCustomSessionStore(f.ProductID, f.Environment, options)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewCustomSessionStore(f.ProductID, f.AlternateEnvironment, options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key, value, expires := f.RecordKey, []byte(f.Secret), time.Now().Add(time.Minute).UnixMilli()
	if r, err := first.Create(ctx, "login", key, value, expires); r == nil || err != nil {
		t.Fatal(err)
	}
	initial, err := first.CreateSession(ctx, key, value, expires)
	if initial == nil || err != nil {
		t.Fatal(err)
	}
	login, stored := backend.rows[f.LoginStorageKey], backend.rows[f.SessionStorageKey]
	if login.Revision == "" || !bytes.HasPrefix(stored.Value, []byte("hr1.old.")) || bytes.Contains(stored.Value, value) {
		t.Fatal("missing scope or encryption")
	}
	if r, err := second.GetSession(ctx, key); err != nil || r == nil || !bytes.Equal(r.Value, value) {
		t.Fatal(err)
	}
	if r, err := other.GetSession(ctx, key); err != nil || r != nil {
		t.Fatal("scope leak", err)
	}
	results := make(chan bool, 2)
	for _, store := range []*customSessionStore{first, second} {
		go func() {
			r, err := store.CompareAndSwapSession(ctx, key, initial.Revision, value, expires)
			if err != nil {
				t.Error(err)
			}
			results <- r != nil
		}()
	}
	if (<-results) == (<-results) || reservations != 4 {
		t.Fatal("expected one winner and metering before both writes")
	}
	stored = backend.rows[f.SessionStorageKey]
	corrupt := []StoredSessionRecord{login, {Value: stored.Value, Revision: stored.Revision, ExpiresAt: stored.ExpiresAt + 1}}
	for _, revision := range f.InvalidRevisions {
		corrupt = append(corrupt, StoredSessionRecord{Value: stored.Value, Revision: revision, ExpiresAt: stored.ExpiresAt})
	}
	for _, record := range corrupt {
		backend.rows[f.SessionStorageKey] = record
		if _, err := first.GetSession(ctx, key); !errors.Is(err, contract.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	backend.rows[f.SessionStorageKey] = stored
	if err := second.DeleteSession(ctx, key); err != nil {
		t.Fatal(err)
	}
	if r, err := first.CompareAndSwapSession(ctx, key, stored.Revision, value, expires); err != nil || r != nil {
		t.Fatal("resurrected", err)
	}
	if r, err := second.CreateSession(ctx, key, value, expires); err != nil || r == nil || r.Revision == initial.Revision {
		t.Fatal("recreated revision", err)
	}
	if !bytes.HasPrefix(backend.rows[f.SessionStorageKey].Value, []byte("hr1.next.")) {
		t.Fatal("key was not rotated")
	}
	if r, err := first.GetSession(ctx, key); err != nil || r == nil || !bytes.Equal(r.Value, value) {
		t.Fatal(err)
	}
	var sessions []struct{ Record map[string]any }
	raw, err = os.ReadFile("../../../../contracts/fixtures/session-v1.json")
	if err != nil || json.Unmarshal(raw, &sessions) != nil {
		t.Fatal("load session fixture", err)
	}
	payload := sessions[0].Record
	payload["idleExpiresAt"] = expires
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	initial, err = first.CreateSession(ctx, "invalidate", encoded, expires)
	if err != nil || initial == nil {
		t.Fatal(err)
	}
	updated, err := second.CompareAndSwapSession(ctx, "invalidate", initial.Revision, encoded, expires)
	if err != nil || updated == nil {
		t.Fatal(err)
	}
	actor := identity.Identity{Issuer: payload["issuer"].(string), Subject: payload["subject"].(string)}
	if err := first.InvalidateSessionVersion(ctx, &identity.SessionContext{Key: "invalidate", Revision: initial.Revision}, actor); err != nil {
		t.Fatal(err)
	}
	if r, err := second.GetSession(ctx, "invalidate"); err != nil || r == nil || r.Revision != updated.Revision {
		t.Fatal("replacement lost", err)
	}
	if err := first.InvalidateSessionVersion(ctx, &identity.SessionContext{Key: "invalidate", Revision: updated.Revision}, actor); err != nil {
		t.Fatal(err)
	}
	if r, err := second.GetSession(ctx, "invalidate"); err != nil || r != nil {
		t.Fatal("invalidation failed", err)
	}
}

func NewMemorySessionStoreForTest() AtomicSessionStore {
	return &memorySessionStore{rows: make(map[string]StoredSessionRecord)}
}
