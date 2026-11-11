package postgres

import (
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

// IdentityRecords persists denial and exact-session invalidation without encryption keys.
type IdentityRecords struct {
	db                   *sql.DB
	product, environment string
	now                  func() int64
}

func NewIdentityRecords(db *sql.DB, product, environment string, now func() int64) (*IdentityRecords, error) {
	if db == nil || !valid(product, 64) || !valid(environment, 64) {
		return nil, contract.ErrInvalidArgument
	}
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &IdentityRecords{db, product, environment, now}, nil
}
func sessionKeyHash(key string) ([]byte, error) {
	if !valid(key, 128) {
		return nil, contract.ErrInvalidArgument
	}
	hash := sha256.Sum256([]byte(key))
	return hash[:], nil
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
func (s *IdentityRecords) InvalidateIdentity(ctx context.Context, issuer, subject string, observedAt int64, reason string) (int64, error) {
	if observedAt < 0 || observedAt > maxSafe || (reason != "inactive" && reason != "deleted") {
		return 0, contract.ErrInvalidArgument
	}
	identity, err := identityBinding(issuer, subject)
	if err != nil {
		return 0, err
	}
	r := s
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

func (s *IdentityRecords) IsIdentityDenied(ctx context.Context, issuer, subject string) (bool, error) {
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
	err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$3)", s.product, s.environment, identity).Scan(&denied)
	if err != nil {
		return false, databaseError(ctx)
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return false, err
	}
	return denied, nil
}

// InvalidateSessionVersion only removes the identity/revision that was observed.
func (s *IdentityRecords) InvalidateSessionVersion(ctx context.Context, key, revision, issuer, subject string) (bool, error) {
	rev, err := strconv.ParseInt(revision, 10, 64)
	if err != nil || rev <= 0 || strconv.FormatInt(rev, 10) != revision {
		return false, contract.ErrInvalidArgument
	}
	hash, err := sessionKeyHash(key)
	if err != nil {
		return false, err
	}
	identity, err := identityBinding(issuer, subject)
	if err != nil {
		return false, err
	}
	r := s
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
func (s *IdentityRecords) IdentityGate(product, environment string) (identity.Gate, error) {
	if s == nil || product != s.product || environment != s.environment {
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
			_, e = s.InvalidateIdentity(ctx, n.Issuer, n.Subject, s.now(), reason)
			return e
		},
	}, nil
}
