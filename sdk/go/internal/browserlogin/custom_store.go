package browserlogin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

// StoredSessionRecord contains an opaque encrypted value. Expiration is Unix milliseconds.
type StoredSessionRecord struct {
	Value     []byte
	Revision  string
	ExpiresAt int64
}

// SessionStore coordinates atomic writes across replicas. A write returns an
// empty revision only when it definitively did not apply.
type AtomicSessionStore interface {
	Create(ctx context.Context, key string, value []byte, expiresAt int64) (revision string, err error)
	Read(ctx context.Context, key string) (*StoredSessionRecord, error)
	CompareAndSwap(ctx context.Context, key, revision string, value []byte, expiresAt int64) (newRevision string, err error)
	Delete(ctx context.Context, key string) error
}

type SessionStoreOptions struct {
	Store           AtomicSessionStore
	ActiveKeyID     string
	Keys            map[string][]byte
	ReserveKeyUsage func(context.Context, string) error
}

type customSessionStore struct {
	product, environment string
	store                AtomicSessionStore
	cipher               *contract.RecordCipher
	fingerprint          string
	reserve              func(context.Context, string) error
}

func NewCustomSessionStore(product, environment string, options SessionStoreOptions) (*customSessionStore, error) {
	if options.Store == nil || options.ReserveKeyUsage == nil {
		return nil, contract.ErrInvalidArgument
	}
	cipher, err := contract.NewRecordCipher(options.ActiveKeyID, options.Keys)
	if err != nil {
		return nil, err
	}
	return &customSessionStore{product, environment, options.Store, cipher, cipher.ActiveFingerprint(), options.ReserveKeyUsage}, nil
}

func (s *customSessionStore) MatchesScope(product, environment string) bool {
	return s.product == product && s.environment == environment
}

func (s *customSessionStore) keyFor(kind, key string) (string, contract.RecordScope) {
	hash := sha256.Sum256([]byte(key))
	recordKey := hex.EncodeToString(hash[:])
	return "heimdall.v1|" + s.product + "|" + s.environment + "|" + kind + "|" + recordKey,
		contract.RecordScope{ProductID: s.product, Environment: s.environment, Kind: kind, RecordKey: recordKey}
}

func sessionStoreCall[T any](ctx context.Context, write bool, call func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return zero, err
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		if err := contract.CheckCancellation(ctx); err != nil {
			done <- result{err: err}
			return
		}
		value, err := call(ctx)
		done <- result{value, err}
	}()
	select {
	case <-ctx.Done():
	case r := <-done:
		if ctx.Err() == nil {
			if r.err == nil {
				return r.value, nil
			}
			var safe *contract.Error
			if errors.As(r.err, &safe) {
				return zero, contract.NewError(safe.Code, "")
			}
			return zero, contract.ErrUnavailable
		}
	}
	if write {
		return zero, contract.ErrOutcomeUnknown
	}
	return zero, contract.CheckCancellation(ctx)
}

func (s *customSessionStore) write(ctx context.Context, kind, key, previous string, value []byte, expires int64) (*postgres.Record, error) {
	storageKey, scope := s.keyFor(kind, key)
	if _, err := sessionStoreCall(ctx, false, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.reserve(ctx, s.fingerprint)
	}); err != nil {
		return nil, err
	}
	if expires <= time.Now().UnixMilli() {
		return nil, nil
	}
	raw, err := json.Marshal([]any{expires, base64.RawURLEncoding.EncodeToString(value)})
	if err != nil {
		return nil, contract.ErrInvalidArgument
	}
	envelope, err := s.cipher.Seal(scope, raw)
	if err != nil {
		return nil, err
	}
	revision, err := sessionStoreCall(ctx, true, func(ctx context.Context) (string, error) {
		if previous == "" {
			return s.store.Create(ctx, storageKey, []byte(envelope), expires)
		}
		return s.store.CompareAndSwap(ctx, storageKey, previous, []byte(envelope), expires)
	})
	if err != nil || revision == "" {
		return nil, err
	}
	if _, err = identity.SessionIdentityContext(key, revision); err != nil || revision == previous {
		return nil, contract.ErrUnavailable
	}
	return &postgres.Record{Value: value, Revision: revision, ExpiresAt: expires}, nil
}

func (s *customSessionStore) Get(ctx context.Context, kind, key string) (*postgres.Record, error) {
	storageKey, scope := s.keyFor(kind, key)
	record, err := sessionStoreCall(ctx, false, func(ctx context.Context) (*StoredSessionRecord, error) {
		return s.store.Read(ctx, storageKey)
	})
	if err != nil || record == nil {
		return nil, err
	}
	if _, err := identity.SessionIdentityContext(key, record.Revision); err != nil {
		return nil, contract.ErrUnavailable
	}
	raw, err := s.cipher.Open(scope, string(record.Value))
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	var fields []json.RawMessage
	var expires int64
	var encoded string
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 2 ||
		json.Unmarshal(fields[0], &expires) != nil || expires != record.ExpiresAt ||
		json.Unmarshal(fields[1], &encoded) != nil || string(fields[1]) == "null" {
		return nil, contract.ErrUnavailable
	}
	value, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(value) != encoded {
		return nil, contract.ErrUnavailable
	}
	if expires <= time.Now().UnixMilli() || len(value) == 0 {
		return nil, nil
	}
	return &postgres.Record{Value: value, Revision: record.Revision, ExpiresAt: expires}, nil
}

func (s *customSessionStore) InvalidateSessionVersion(ctx context.Context, observed *identity.SessionContext, actor identity.Identity) error {
	record, err := s.GetSession(ctx, observed.Key)
	if err != nil || record == nil || record.Revision != observed.Revision {
		return err
	}
	principal, err := SessionPrincipal(record.Value)
	if err != nil || principal.Issuer != actor.Issuer || principal.Subject != actor.Subject {
		return contract.ErrUnavailable
	}
	// A tombstone at the observed version leaves concurrent replacements intact.
	_, err = s.CompareAndSwapSession(ctx, observed.Key, observed.Revision, nil, record.ExpiresAt)
	return err
}

func (s *customSessionStore) Create(ctx context.Context, kind, key string, value []byte, expires int64) (*postgres.Record, error) {
	return s.write(ctx, kind, key, "", value, expires)
}

func (s *customSessionStore) CompareAndSwap(ctx context.Context, kind, key, revision string, value []byte, expires int64) (*postgres.Record, error) {
	return s.write(ctx, kind, key, revision, value, expires)
}

func (s *customSessionStore) CreateSession(ctx context.Context, key string, value []byte, expires int64) (*postgres.Record, error) {
	return s.Create(ctx, "session", key, value, expires)
}

func (s *customSessionStore) GetSession(ctx context.Context, key string) (*postgres.Record, error) {
	return s.Get(ctx, "session", key)
}

func (s *customSessionStore) CompareAndSwapSession(ctx context.Context, key, revision string, value []byte, expires int64) (*postgres.Record, error) {
	return s.CompareAndSwap(ctx, "session", key, revision, value, expires)
}

func (s *customSessionStore) DeleteSession(ctx context.Context, key string) error {
	storageKey, _ := s.keyFor("session", key)
	_, err := sessionStoreCall(ctx, true, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.store.Delete(ctx, storageKey)
	})
	return err
}
