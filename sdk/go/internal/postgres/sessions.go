package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"strconv"
	"time"
)

type SessionPrincipal struct{ ProductID, Environment, Issuer, Subject string }

// SessionRecords binds validated payloads to their identity index. Authentication
// and provider freshness are still the calling SDK layer's responsibility.
type SessionRecords struct {
	*IdentityRecords
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
	identities, err := NewIdentityRecords(db, product, environment, r.now)
	if err != nil {
		return nil, err
	}
	return &SessionRecords{identities, r, validate}, nil
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
