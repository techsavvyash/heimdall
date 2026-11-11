package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/lib/pq"
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
	if recordedAt != receipt.RecordedAt {
		return empty, contract.ErrAuditUnavailable
	}
	if _, err = a.decodeStored(storedEvent, eventID, recordedAt, digest); err != nil {
		return empty, err
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

// Acknowledge accepts a validated transport receipt for the exact submitted batch.
// deliveredAt is the local acknowledgement time, not an untrusted sink timestamp.
func (a *Audit) Acknowledge(ctx context.Context, batch, receipt contract.AuditDelivery, deliveredAt int64) error {
	shape := func(v contract.AuditDelivery) bool {
		if !valid(v.ProductID, 64) || !valid(v.Environment, 64) || !valid(v.SinkID, 128) || len(v.EventIDs) < 1 || len(v.EventIDs) > 1000 {
			return false
		}
		seen := map[string]bool{}
		for _, id := range v.EventIDs {
			if !valid(id, 128) || seen[id] {
				return false
			}
			seen[id] = true
		}
		return true
	}
	if !shape(batch) || batch.ProductID != a.product || batch.Environment != a.environment || deliveredAt < 0 || deliveredAt > maxSafe {
		return contract.ErrInvalidArgument
	}
	if !shape(receipt) {
		return contract.ErrUnavailable
	}
	if err := contract.ValidateDeliveryReceipt(batch, receipt); err != nil {
		return err
	}
	ids := append([]string{}, receipt.EventIDs...)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); err != nil {
		return databaseError(ctx)
	}
	rows, err := tx.QueryContext(ctx, "SELECT state FROM heimdall.audit_deliveries WHERE product_id=$1 AND environment=$2 AND sink_id=$3 AND event_id=ANY($4::text[]) ORDER BY event_id FOR UPDATE", a.product, a.environment, batch.SinkID, pq.Array(ids))
	if err != nil {
		return databaseError(ctx)
	}
	count := 0
	pending := 0
	validStates := true
	for rows.Next() {
		var state string
		if err = rows.Scan(&state); err != nil {
			rows.Close()
			return databaseError(ctx)
		}
		count++
		if state == "pending" {
			pending++
		}
		if state != "pending" && state != "delivered" {
			validStates = false
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return databaseError(ctx)
	}
	if count != len(ids) || !validStates {
		return contract.ErrUnavailable
	}
	updated, err := tx.ExecContext(ctx, "UPDATE heimdall.audit_deliveries SET state='delivered',delivered_at=$5 WHERE product_id=$1 AND environment=$2 AND sink_id=$3 AND event_id=ANY($4::text[]) AND state='pending'", a.product, a.environment, batch.SinkID, pq.Array(ids), deliveredAt)
	if err != nil {
		return databaseError(ctx)
	}
	changed, err := updated.RowsAffected()
	if err != nil || changed != int64(pending) {
		return contract.ErrUnavailable
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return contract.ErrOutcomeUnknown
	}
	return nil
}

func (a *Audit) decodeStored(raw []byte, eventID string, recordedAt int64, digest string) (map[string]any, error) {
	var wrapper map[string]any
	if json.Unmarshal(raw, &wrapper) != nil || len(wrapper) != 6 || wrapper["format"] != "ha1" || wrapper["productId"] != a.product || wrapper["environment"] != a.environment || wrapper["eventId"] != eventID || wrapper["recordedAt"] != float64(recordedAt) {
		return nil, contract.ErrAuditUnavailable
	}
	payload, ok := wrapper["payload"].(string)
	if !ok {
		return nil, contract.ErrAuditUnavailable
	}
	bytes, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil || base64.RawURLEncoding.EncodeToString(bytes) != payload {
		return nil, contract.ErrAuditUnavailable
	}
	var event map[string]any
	if json.Unmarshal(bytes, &event) != nil {
		return nil, contract.ErrAuditUnavailable
	}
	if a.validate(event) != nil || event["productId"] != a.product || event["environment"] != a.environment || event["eventId"] != eventID || event["recordedAt"] != float64(recordedAt) {
		return nil, contract.ErrAuditUnavailable
	}
	actual, err := contract.AuditDigest(event)
	if err != nil || actual != digest {
		return nil, contract.ErrAuditUnavailable
	}
	return event, nil
}

// PendingAudit has a nil Event only when stored data failed validation. Never
// export such a payload; its scoped identity permits explicit quarantine.
type PendingAudit struct {
	EventID  string
	Attempts int
	Event    map[string]any
}

// Pending is a read, not a lease. Duplicates from concurrent workers are expected.
func (a *Audit) Pending(ctx context.Context, sink string, now int64, limit int) ([]PendingAudit, error) {
	if !valid(sink, 128) || now < 0 || now > maxSafe || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	rows, err := a.db.QueryContext(ctx, `WITH candidates AS MATERIALIZED (
 SELECT d.event_id,d.attempts,d.next_attempt_at,e.recorded_at,e.payload_digest,e.event
 FROM heimdall.audit_deliveries d JOIN heimdall.audit_events e USING(product_id,environment,event_id)
 WHERE d.product_id=$1 AND d.environment=$2 AND d.sink_id=$3 AND d.state='pending' AND d.next_attempt_at<=$4
 ORDER BY d.next_attempt_at,d.event_id COLLATE "C" LIMIT $5
 ), sized AS (
 SELECT *,sum(octet_length(event::text)) OVER(ORDER BY next_attempt_at,event_id COLLATE "C") AS size FROM candidates
 ) SELECT event_id,attempts,recorded_at,payload_digest,event FROM sized WHERE size<=4194304 ORDER BY next_attempt_at,event_id COLLATE "C"`, a.product, a.environment, sink, now, limit)
	if err != nil {
		return nil, databaseError(ctx)
	}
	defer rows.Close()
	result := []PendingAudit{}
	for rows.Next() {
		var item PendingAudit
		var recordedAt int64
		var digest, raw []byte
		if err = rows.Scan(&item.EventID, &item.Attempts, &recordedAt, &digest, &raw); err != nil {
			return nil, databaseError(ctx)
		}
		item.Event, _ = a.decodeStored(raw, item.EventID, recordedAt, hex.EncodeToString(digest))
		result = append(result, item)
	}
	if rows.Err() != nil {
		return nil, databaseError(ctx)
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
