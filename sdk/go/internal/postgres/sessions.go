package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"regexp"
	"strconv"
	"time"
)

type SessionPrincipal struct{ ProductID, Environment, Issuer, Subject string }

// SessionRecords binds validated payloads to their identity index. Authentication
// and provider freshness are still the calling SDK layer's responsibility.
type SessionRecords struct {
	records  *Records
	validate func([]byte) (SessionPrincipal, error)
}

func NewSessionRecords(db *sql.DB, cipher *contract.RecordCipher, product, environment string, validate func([]byte) (SessionPrincipal, error), now func() int64) (*SessionRecords, error) {
	r, err := NewRecords(db, cipher, product, environment, now)
	if err != nil {
		return nil, err
	}
	if validate == nil {
		return nil, contract.ErrInvalidArgument
	}
	return &SessionRecords{r, validate}, nil
}
func identityBinding(issuer, subject string) ([]byte, error) {
	encoded, err := contract.EncodeIdentity(issuer, subject)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(encoded))
	return hash[:], nil
}
func identityLock(product, environment string, identity []byte) int64 {
	hash := sha256.Sum256([]byte("heimdall.identity.v1|" + product + "|" + environment + "|" + hex.EncodeToString(identity)))
	return int64(binary.BigEndian.Uint64(hash[:8]))
}
func (s *SessionRecords) principal(value []byte) ([]byte, error) {
	p, err := s.validate(value)
	if err != nil {
		return nil, err
	}
	if p.ProductID != s.records.product || p.Environment != s.records.environment {
		return nil, contract.ErrInvalidArgument
	}
	return identityBinding(p.Issuer, p.Subject)
}
func identityTransaction[T any](ctx context.Context, db *sql.DB, lock int64, action func(context.Context, *sql.Tx) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return zero, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return zero, databaseError(ctx)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); err != nil {
		return zero, databaseError(ctx)
	}
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1::bigint)", lock); err != nil {
		return zero, databaseError(ctx)
	}
	result, err := action(ctx, tx)
	if err != nil {
		var known *contract.Error
		if errors.As(err, &known) {
			return zero, known
		}
		var pgerr *pq.Error
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			return zero, contract.ErrConflict
		}
		return zero, databaseError(ctx)
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, contract.ErrOutcomeUnknown
	}
	return result, nil
}
func (s *SessionRecords) openSession(scope contract.RecordScope, row *sql.Row) (*Record, error) {
	var revision, envelope, expiry, keyID string
	var identity []byte
	if err := row.Scan(&revision, &envelope, &expiry, &keyID, &identity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(identity) != 32 {
		return nil, contract.ErrUnavailable
	}
	scope.RecordKey += hex.EncodeToString(identity)
	record, err := s.records.open(scope, revision, envelope, expiry, keyID)
	if err != nil || record == nil {
		return record, err
	}
	expected, err := s.principal(record.Value)
	if err != nil || !bytes.Equal(expected, identity) {
		return nil, contract.ErrUnavailable
	}
	return record, nil
}
func (s *SessionRecords) CreateSession(ctx context.Context, key string, value []byte, expires int64) (*Record, error) {
	return s.writeSession(ctx, key, "", value, expires)
}
func (s *SessionRecords) CompareAndSwapSession(ctx context.Context, key, revision string, value []byte, expires int64) (*Record, error) {
	rev, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || rev <= 0 || strconv.FormatInt(rev, 10) != revision {
		return nil, contract.ErrInvalidArgument
	}
	return s.writeSession(ctx, key, revision, value, expires)
}
func (s *SessionRecords) writeSession(ctx context.Context, key, revision string, input []byte, expires int64) (*Record, error) {
	value := append([]byte{}, input...)
	r := s.records
	scope, hash, err := r.scope("session", key)
	if err != nil {
		return nil, err
	}
	identity, err := s.principal(value)
	if err != nil {
		return nil, err
	}
	boundScope := scope
	boundScope.RecordKey += hex.EncodeToString(identity)
	envelope, keyID, err := r.seal(boundScope, value, expires)
	if err != nil {
		return nil, err
	}
	return identityTransaction(ctx, r.db, identityLock(r.product, r.environment, identity), func(ctx context.Context, tx *sql.Tx) (*Record, error) {
		var denied bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$3)", r.product, r.environment, identity).Scan(&denied); err != nil {
			return nil, err
		}
		if denied {
			return nil, contract.ErrUnauthenticated
		}
		args := []any{r.product, r.environment, hash, keyID, envelope, expires, identity}
		if revision == "" {
			return s.openSession(scope, tx.QueryRowContext(ctx, "INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at,identity_hash) VALUES($1,$2,'session',$3,$4,$5,$6,$7) RETURNING "+columns, args...))
		}
		return s.openSession(scope, tx.QueryRowContext(ctx, "UPDATE heimdall.records SET key_id=$4,envelope=$5,expires_at=$6 WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$3 AND identity_hash=$7 AND revision=$8 AND expires_at>$9 RETURNING "+columns, append(args, revision, r.now())...))
	})
}
func (s *SessionRecords) GetSession(ctx context.Context, key string) (*Record, error) {
	r := s.records
	scope, hash, err := r.scope("session", key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	record, err := s.openSession(scope, r.db.QueryRowContext(ctx, "SELECT "+columns+" FROM heimdall.records r WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$3 AND identity_hash IS NOT NULL AND expires_at>$4 AND NOT EXISTS(SELECT 1 FROM heimdall.identity_denials d WHERE d.product_id=r.product_id AND d.environment=r.environment AND d.identity_hash=r.identity_hash)", r.product, r.environment, hash, r.now()))
	if cancelled := contract.CheckCancellation(ctx); cancelled != nil {
		return nil, cancelled
	}
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	return record, nil
}
func (s *SessionRecords) DeleteSession(ctx context.Context, key string) error {
	r := s.records
	scope, hash, err := r.scope("session", key)
	if err != nil {
		return err
	}
	_, err = r.query(ctx, scope, true, "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$3 AND identity_hash IS NOT NULL", r.product, r.environment, hash)
	return err
}
func (s *SessionRecords) InvalidateIdentity(ctx context.Context, issuer, subject string, observedAt int64, reason string) (int64, error) {
	if observedAt < 0 || observedAt > maxSafe || (reason != "inactive" && reason != "deleted") {
		return 0, contract.ErrInvalidArgument
	}
	identity, err := identityBinding(issuer, subject)
	if err != nil {
		return 0, err
	}
	r := s.records
	return identityTransaction(ctx, r.db, identityLock(r.product, r.environment, identity), func(ctx context.Context, tx *sql.Tx) (int64, error) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO heimdall.identity_denials(product_id,environment,identity_hash,observed_at,reason) VALUES($1,$2,$3,$4,$5) ON CONFLICT(product_id,environment,identity_hash) DO UPDATE SET observed_at=GREATEST(identity_denials.observed_at,EXCLUDED.observed_at),reason=CASE WHEN identity_denials.reason='deleted' OR EXCLUDED.reason='deleted' THEN 'deleted' ELSE 'inactive' END", r.product, r.environment, identity, observedAt, reason); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND identity_hash=$3", r.product, r.environment, identity)
		if err != nil {
			return 0, err
		}
		return result.RowsAffected()
	})
}

func (s *SessionRecords) IsIdentityDenied(ctx context.Context, issuer, subject string) (bool, error) {
	identity, err := identityBinding(issuer, subject)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = contract.CheckCancellation(ctx); err != nil {
		return false, err
	}
	var denied bool
	err = s.records.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$3)", s.records.product, s.records.environment, identity).Scan(&denied)
	if err != nil {
		return false, databaseError(ctx)
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return false, err
	}
	return denied, nil
}

// InvalidateSessionVersion only removes the identity/revision that was observed.
func (s *SessionRecords) InvalidateSessionVersion(ctx context.Context, key, revision, issuer, subject string) (bool, error) {
	rev, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || rev <= 0 || strconv.FormatInt(rev, 10) != revision {
		return false, contract.ErrInvalidArgument
	}
	_, hash, err := s.records.scope("session", key)
	if err != nil {
		return false, err
	}
	identity, err := identityBinding(issuer, subject)
	if err != nil {
		return false, err
	}
	r := s.records
	return identityTransaction(ctx, r.db, identityLock(r.product, r.environment, identity), func(ctx context.Context, tx *sql.Tx) (bool, error) {
		result, err := tx.ExecContext(ctx, "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$3 AND identity_hash=$4 AND revision=$5", r.product, r.environment, hash, identity, revision)
		if err != nil {
			return false, err
		}
		count, err := result.RowsAffected()
		return count == 1, err
	})
}

var identityDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// IdentityGate binds the durable deny/session adapter to a verifier's immutable scope.
func (s *SessionRecords) IdentityGate(product, environment string) (identity.Gate, error) {
	if s == nil || product != s.records.product || environment != s.records.environment {
		return identity.Gate{}, contract.ErrInvalidArgument
	}
	return identity.Gate{
		Check: func(ctx context.Context, p identity.Identity) (bool, error) {
			return s.IsIdentityDenied(ctx, p.Issuer, p.Subject)
		},
		Invalidate: func(ctx context.Context, n identity.Negative) error {
			if e := contract.CheckCancellation(ctx); e != nil {
				return e
			}
			scope, e := identity.ParseIdentityContext(n.Context)
			if e != nil {
				return e
			}
			if !identityDigest.MatchString(n.TokenDigest) {
				return contract.ErrInvalidArgument
			}
			if n.Reason == "token_inactive" {
				if scope != nil {
					_, e = s.InvalidateSessionVersion(ctx, scope.Key, scope.Revision, n.Issuer, n.Subject)
					return e
				}
				return nil
			}
			if n.Reason != "identity_inactive" && n.Reason != "identity_deleted" {
				return contract.ErrInvalidArgument
			}
			reason := "inactive"
			if n.Reason == "identity_deleted" {
				reason = "deleted"
			}
			_, e = s.InvalidateIdentity(ctx, n.Issuer, n.Subject, s.records.now(), reason)
			return e
		},
	}, nil
}
