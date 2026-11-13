// Package postgres implements internal shared persistence primitives.
package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type Records struct {
	db                   *sql.DB
	keyUsageDB           *sql.DB
	cipher               *contract.RecordCipher
	product, environment string
	now                  func() int64
}
type Record struct {
	Revision  string
	ExpiresAt int64
	Value     []byte
}

var token = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const columns = "revision::text, envelope, expires_at::text, key_id, identity_hash"
const maxSafe = 9007199254740991

func valid(s string, max int) bool { return len(s) > 0 && len(s) <= max && token.MatchString(s) }

// NewRecords does not migrate or own the caller's connection pool.
func NewRecords(db, keyUsageDB *sql.DB, cipher *contract.RecordCipher, product, environment string, now func() int64) (*Records, error) {
	if db == nil || keyUsageDB == nil || cipher == nil || !valid(product, 64) || !valid(environment, 64) {
		return nil, contract.ErrInvalidArgument
	}
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Records{db, keyUsageDB, cipher, product, environment, now}, nil
}
func (r *Records) scope(kind, key string) (contract.RecordScope, []byte, error) {
	if (kind != "session" && kind != "login") || !valid(key, 128) {
		return contract.RecordScope{}, nil, contract.ErrInvalidArgument
	}
	hash := sha256.Sum256([]byte(key))
	return contract.RecordScope{ProductID: r.product, Environment: r.environment, Kind: kind, RecordKey: hex.EncodeToString(hash[:])}, hash[:], nil
}
func (r *Records) seal(ctx context.Context, scope contract.RecordScope, value []byte, expires int64) (string, string, error) {
	if len(value) > 700000 || expires > maxSafe || expires <= r.now() {
		return "", "", contract.ErrInvalidArgument
	}
	raw, err := json.Marshal([]any{expires, base64.RawURLEncoding.EncodeToString(value)})
	if err != nil {
		return "", "", contract.ErrInvalidArgument
	}
	if err := ReserveRecordKeyUsage(ctx, r.keyUsageDB, r.cipher.ActiveFingerprint()); err != nil {
		return "", "", err
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return "", "", err
	}
	envelope, err := r.cipher.Seal(scope, raw)
	if err != nil {
		return "", "", err
	}
	return envelope, strings.Split(envelope, ".")[1], nil
}
func (r *Records) query(ctx context.Context, scope contract.RecordScope, write bool, query string, args ...any) (*Record, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	// Acquire separately so a cancelled queue wait is known not to have dispatched SQL.
	conn, err := r.db.Conn(ctx)
	if err != nil {
		if err := contract.CheckCancellation(ctx); err != nil {
			return nil, err
		}
		return nil, contract.ErrUnavailable
	}
	defer conn.Close()
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	var revision, envelope, expiry, keyID string
	var identityHash []byte
	err = conn.QueryRowContext(ctx, query, args...).Scan(&revision, &envelope, &expiry, &keyID, &identityHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		var pgerr *pq.Error
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			return nil, contract.ErrConflict
		}
		if write {
			return nil, contract.ErrOutcomeUnknown
		}
		if err := contract.CheckCancellation(ctx); err != nil {
			return nil, err
		}
		return nil, contract.ErrUnavailable
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		if write {
			return nil, contract.ErrOutcomeUnknown
		}
		return nil, err
	}
	if len(identityHash) != 0 {
		if len(identityHash) != 32 {
			return nil, contract.ErrUnavailable
		}
		scope.RecordKey += hex.EncodeToString(identityHash)
	}
	return r.open(scope, revision, envelope, expiry, keyID)
}
func (r *Records) open(scope contract.RecordScope, revision, envelope, expiry, keyID string) (*Record, error) {
	rev, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || rev <= 0 || strconv.FormatInt(rev, 10) != revision {
		return nil, contract.ErrUnavailable
	}
	parts := strings.Split(envelope, ".")
	if len(parts) != 3 || parts[1] != keyID {
		return nil, contract.ErrUnavailable
	}
	raw, err := r.cipher.Open(scope, envelope)
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	var wrapped []json.RawMessage
	if json.Unmarshal(raw, &wrapped) != nil || len(wrapped) != 2 {
		return nil, contract.ErrUnavailable
	}
	var expires int64
	var encoded string
	if json.Unmarshal(wrapped[0], &expires) != nil || expires < 0 || expires > maxSafe || strconv.FormatInt(expires, 10) != expiry || json.Unmarshal(wrapped[1], &encoded) != nil {
		return nil, contract.ErrUnavailable
	}
	value, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(value) != encoded {
		return nil, contract.ErrUnavailable
	}
	if expires <= r.now() {
		return nil, nil
	}
	return &Record{revision, expires, value}, nil
}
func (r *Records) matchesScope(product, environment string) bool {
	return r.product == product && r.environment == environment
}

func (r *Records) Create(ctx context.Context, kind, key string, value []byte, expires int64) (*Record, error) {
	scope, hash, err := r.scope(kind, key)
	if err != nil {
		return nil, err
	}
	envelope, keyID, err := r.seal(ctx, scope, value, expires)
	if err != nil {
		return nil, err
	}
	return r.query(ctx, scope, true, "INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING "+columns, r.product, r.environment, kind, hash, keyID, envelope, expires)
}
func (r *Records) Get(ctx context.Context, kind, key string) (*Record, error) {
	scope, hash, err := r.scope(kind, key)
	if err != nil {
		return nil, err
	}
	return r.query(ctx, scope, false, "SELECT "+columns+" FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind=$3 AND key_hash=$4 AND identity_hash IS NULL AND expires_at>$5", r.product, r.environment, kind, hash, r.now())
}
func (r *Records) CompareAndSwap(ctx context.Context, kind, key, revision string, value []byte, expires int64) (*Record, error) {
	rev, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || rev <= 0 || strconv.FormatInt(rev, 10) != revision {
		return nil, contract.ErrInvalidArgument
	}
	scope, hash, err := r.scope(kind, key)
	if err != nil {
		return nil, err
	}
	envelope, keyID, err := r.seal(ctx, scope, value, expires)
	if err != nil {
		return nil, err
	}
	return r.query(ctx, scope, true, "UPDATE heimdall.records SET key_id=$5,envelope=$6,expires_at=$7 WHERE product_id=$1 AND environment=$2 AND kind=$3 AND key_hash=$4 AND identity_hash IS NULL AND revision=$8 AND expires_at>$9 RETURNING "+columns, r.product, r.environment, kind, hash, keyID, envelope, expires, revision, r.now())
}
func (r *Records) Delete(ctx context.Context, kind, key string) error {
	scope, hash, err := r.scope(kind, key)
	if err != nil {
		return err
	}
	// DELETE has no rows; the shared query path still classifies uncertain writes.
	_, err = r.query(ctx, scope, true, "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind=$3 AND key_hash=$4 AND identity_hash IS NULL", r.product, r.environment, kind, hash)
	return err
}
