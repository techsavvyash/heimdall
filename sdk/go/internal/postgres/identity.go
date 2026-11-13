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
	retainedRuntime      bool
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
	return &IdentityRecords{db: db, product: product, environment: environment, now: now}, nil
}

// NewRetainedRuntimeIdentityRecords applies v4 admission to public identity storage.
func NewRetainedRuntimeIdentityRecords(db *sql.DB, product, environment string) (*IdentityRecords, error) {
	r, e := NewIdentityRecords(db, product, environment, nil)
	if e != nil {
		return nil, e
	}
	r.retainedRuntime = true
	return r, nil
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
func identityTransaction[T any](ctx context.Context, db *sql.DB, lock int64, retained bool, action func(context.Context, *sql.Tx) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return zero, err
	}
	conn, err := retainedConnection(ctx, db, retained)
	if err != nil {
		return zero, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return zero, databaseError(ctx)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); err != nil {
		return zero, databaseError(ctx)
	}
	if retained {
		if err = lockRetentionAdmission(ctx, tx); err != nil {
			return zero, err
		}
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
	return identityTransaction(ctx, r.db, identityLock(r.product, r.environment, identity), r.retainedRuntime, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		return r.invalidateLocked(ctx, tx, identity, observedAt, reason)
	})
}

func (s *IdentityRecords) invalidateLocked(ctx context.Context, tx *sql.Tx, identity []byte, observedAt int64, reason string) (int64, error) {
	if _, err := tx.ExecContext(ctx, "INSERT INTO heimdall.identity_denials(product_id,environment,identity_hash,observed_at,reason) VALUES($1,$2,$3,$4,$5) ON CONFLICT(product_id,environment,identity_hash) DO UPDATE SET observed_at=GREATEST(identity_denials.observed_at,EXCLUDED.observed_at),reason=CASE WHEN identity_denials.reason='deleted' OR EXCLUDED.reason='deleted' THEN 'deleted' ELSE 'inactive' END", s.product, s.environment, identity, observedAt, reason); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND identity_hash=$3", s.product, s.environment, identity)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *IdentityRecords) RecordDirectoryDeactivation(ctx context.Context, issuer, subject, org, state string, at int64) (int64, error) {
	if s == nil || ctx == nil || !valid(subject, 128) || !valid(org, 128) ||
		at < 0 || at > maxSafe || (state != "inactive" && state != "missing") {
		return 0, contract.ErrInvalidArgument
	}
	reason := "inactive"
	if state == "missing" {
		reason = "deleted"
	}
	binding, err := identityBinding(issuer, subject)
	if err != nil {
		return 0, err
	}
	return identityTransaction(ctx, s.db, identityLock(s.product, s.environment, binding), true, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		result, err := tx.ExecContext(ctx, `INSERT INTO heimdall_directory.deactivations(product_id,environment,identity_hash,issuer,subject,organization_id,observed_at,reason)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(product_id,environment,identity_hash) DO UPDATE
SET observed_at=GREATEST(deactivations.observed_at,EXCLUDED.observed_at),
    revision=CASE WHEN deactivations.reactivation_started_at IS NOT NULL OR (deactivations.reason='inactive' AND EXCLUDED.reason='deleted') THEN EXCLUDED.revision ELSE deactivations.revision END,
    completed=deactivations.completed AND NOT (deactivations.reactivation_started_at IS NOT NULL OR (deactivations.reason='inactive' AND EXCLUDED.reason='deleted')),
    cleanup_plan=CASE WHEN deactivations.reactivation_started_at IS NOT NULL OR (deactivations.reason='inactive' AND EXCLUDED.reason='deleted') THEN NULL ELSE deactivations.cleanup_plan END,
    reactivation_started_at=NULL,
    reason=CASE WHEN deactivations.reason='deleted' OR EXCLUDED.reason='deleted' THEN 'deleted' ELSE 'inactive' END
WHERE deactivations.issuer=EXCLUDED.issuer AND deactivations.subject=EXCLUDED.subject
  AND deactivations.organization_id=EXCLUDED.organization_id`, s.product, s.environment, binding, issuer, subject, org, at, reason)
		if err != nil {
			return 0, err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return 0, contract.ErrConflict
		}
		return s.invalidateLocked(ctx, tx, binding, at, reason)
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
	if s.retainedRuntime {
		var ready bool
		args := append(retentionReadinessArgs(), s.product, s.environment, identity)
		err = s.db.QueryRowContext(ctx, "SELECT readiness.ready, EXISTS(SELECT 1 FROM heimdall.identity_denials WHERE product_id=$7 AND environment=$8 AND identity_hash=$9) FROM ("+retentionReadinessSQL+") readiness", args...).Scan(&ready, &denied)
		if err == nil && !ready {
			return false, contract.ErrUnavailable
		}
	} else {
		err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$3)", s.product, s.environment, identity).Scan(&denied)
	}
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
	return identityTransaction(ctx, r.db, identityLock(r.product, r.environment, identity), r.retainedRuntime, func(ctx context.Context, tx *sql.Tx) (bool, error) {
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
