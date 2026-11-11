package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"math"
	"sort"
	"time"
)

// Audit is an internal persistence port. Validation must include schema and
// semantics; verified actor/origin are supplied by the authenticated SDK layer.
type Audit struct {
	db                   *sql.DB
	product, environment string
	sinks                []string
	validate             func(map[string]any) error
}

func NewAudit(db *sql.DB, product, environment string, sinks []string, validate func(map[string]any) error) (*Audit, error) {
	if db == nil || !valid(product, 64) || !valid(environment, 64) || len(sinks) > 32 || validate == nil {
		return nil, contract.ErrInvalidArgument
	}
	seen := map[string]bool{}
	for _, sink := range sinks {
		if !valid(sink, 128) || seen[sink] {
			return nil, contract.ErrInvalidArgument
		}
		seen[sink] = true
	}
	copySinks := append([]string{}, sinks...)
	sort.Strings(copySinks)
	return &Audit{db, product, environment, copySinks, validate}, nil
}
func (a *Audit) Append(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
	empty := contract.AuditReceipt{}
	digest, err := contract.AuditDigest(event)
	if err != nil {
		return empty, err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return empty, contract.ErrInvalidArgument
	}
	var snapshot map[string]any
	if json.Unmarshal(encoded, &snapshot) != nil {
		return empty, contract.ErrInvalidArgument
	}
	if err = a.validate(snapshot); err != nil {
		return empty, err
	}
	clonedDigest, err := contract.AuditDigest(snapshot)
	if err != nil || clonedDigest != digest {
		return empty, contract.ErrInvalidArgument
	}
	eventID, ok := snapshot["eventId"].(string)
	at, number := snapshot["recordedAt"].(float64)
	if !ok || !valid(eventID, 128) || !number || math.Trunc(at) != at || at < 0 || at > maxSafe || snapshot["productId"] != a.product || snapshot["environment"] != a.environment {
		return empty, contract.ErrInvalidArgument
	}
	receipt := contract.AuditReceipt{ProductID: a.product, Environment: a.environment, EventID: eventID, RecordedAt: int64(at), PayloadDigest: digest}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = contract.CheckCancellation(ctx); err != nil {
		return empty, err
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return empty, databaseError(ctx)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); err != nil {
		return empty, databaseError(ctx)
	}
	storageEnvelope, _ := json.Marshal(map[string]any{"format": "ha1", "productId": a.product, "environment": a.environment, "eventId": eventID, "recordedAt": receipt.RecordedAt, "payload": base64.RawURLEncoding.EncodeToString(encoded)})
	digestBytes, _ := hex.DecodeString(digest)
	inserted, err := tx.ExecContext(ctx, "INSERT INTO heimdall.audit_events(product_id,environment,event_id,payload_digest,recorded_at,event) VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT DO NOTHING", a.product, a.environment, eventID, digestBytes, receipt.RecordedAt, string(storageEnvelope))
	if err != nil {
		return empty, databaseError(ctx)
	}
	var storedDigest, storedEvent []byte
	var recordedAt int64
	err = tx.QueryRowContext(ctx, "SELECT payload_digest,recorded_at,event FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2 AND event_id=$3", a.product, a.environment, eventID).Scan(&storedDigest, &recordedAt, &storedEvent)
	if err != nil {
		return empty, databaseError(ctx)
	}
	if hex.EncodeToString(storedDigest) != digest {
		return empty, contract.ErrConflict
	}
	var wrapper map[string]any
	if json.Unmarshal(storedEvent, &wrapper) != nil || len(wrapper) != 6 || wrapper["format"] != "ha1" || wrapper["productId"] != a.product || wrapper["environment"] != a.environment || wrapper["eventId"] != eventID || wrapper["recordedAt"] != at {
		return empty, contract.ErrAuditUnavailable
	}
	payload, ok := wrapper["payload"].(string)
	if !ok {
		return empty, contract.ErrAuditUnavailable
	}
	bytes, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil || base64.RawURLEncoding.EncodeToString(bytes) != payload {
		return empty, contract.ErrAuditUnavailable
	}
	var stored map[string]any
	if json.Unmarshal(bytes, &stored) != nil {
		return empty, contract.ErrAuditUnavailable
	}
	actual, err := contract.AuditDigest(stored)
	if err != nil || actual != digest || recordedAt != receipt.RecordedAt || a.validate(stored) != nil {
		return empty, contract.ErrAuditUnavailable
	}
	count, err := inserted.RowsAffected()
	if err != nil {
		return empty, contract.ErrUnavailable
	}
	if count == 1 {
		for _, sink := range a.sinks {
			if _, err = tx.ExecContext(ctx, "INSERT INTO heimdall.audit_deliveries(product_id,environment,event_id,sink_id) VALUES($1,$2,$3,$4)", a.product, a.environment, eventID, sink); err != nil {
				return empty, databaseError(ctx)
			}
		}
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, contract.ErrOutcomeUnknown
	}
	return receipt, nil
}
