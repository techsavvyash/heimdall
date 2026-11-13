package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

// AppendRetained is a prepared compatible writer, not public v4 activation.
// New recovery pairs use RecoverRetained to preserve atomic administration.
func (a *Audit) AppendRetained(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
	return a.appendRetainedWith(ctx, event, func(context.Context, *sql.Tx) error { return nil })
}

// Local SDK mutations retain their own operation-key and generation checks.
func (a *Audit) appendRetainedWith(ctx context.Context, event map[string]any, mutate func(context.Context, *sql.Tx) error) (contract.AuditReceipt, error) {
	return a.appendRetainedFinalized(ctx, event, mutate, func(context.Context, *sql.Tx) error { return nil })
}

func (a *Audit) appendRetainedFinalized(ctx context.Context, event map[string]any, mutate, finalize func(context.Context, *sql.Tx) error) (contract.AuditReceipt, error) {
	empty := contract.AuditReceipt{}
	if ctx == nil || mutate == nil || finalize == nil {
		return empty, contract.ErrInvalidArgument
	}
	p, e := a.prepare(event)
	if e != nil {
		return empty, e
	}
	e = a.retainedWriteTransaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if e = mutate(ctx, tx); e != nil {
			var known *contract.Error
			if errors.As(e, &known) {
				return known
			}
			var pgerr *pq.Error
			if errors.As(e, &pgerr) && pgerr.Code == "23505" {
				return contract.ErrConflict
			}
			return databaseError(ctx)
		}
		if err := a.appendPreparedRetained(ctx, tx, p); err != nil {
			return err
		}
		return finalize(ctx, tx)
	})
	if e != nil {
		return empty, e
	}
	return p.receipt, nil
}

func (a *Audit) retainedWriteTransaction(ctx context.Context, write func(context.Context, *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return e
	}
	conn, e := retainedConnection(ctx, a.db, a.retainedRuntime)
	if e != nil {
		return databaseError(ctx)
	}
	defer conn.Close()
	tx, e := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); e != nil {
		return databaseError(ctx)
	}
	if e := a.lockRetainedCapacity(ctx, tx); e != nil {
		return e
	}
	if e = write(ctx, tx); e != nil {
		return e
	}
	if e := contract.CheckCancellation(ctx); e != nil {
		return e
	}
	if e := tx.Commit(); e != nil {
		return contract.ErrOutcomeUnknown
	}
	return nil
}

func (a *Audit) appendPreparedRetained(ctx context.Context, tx *sql.Tx, p *preparedAudit) error {
	var tenant *string
	if v, ok := p.snapshot["tenantId"].(string); ok {
		tenant = &v
	}
	read := func(id string) (*RetainedEventInspection, error) { return a.readRetainedTx(ctx, tx, id) }
	existing, e := read(p.receipt.EventID)
	if e != nil {
		return e
	}
	if existing != nil {
		if existing.Identity.Digest != p.receipt.PayloadDigest || existing.Identity.RecordedAt != p.receipt.RecordedAt {
			return contract.ErrConflict
		}
	} else {
		request := p.snapshot["eventType"] == "heimdall.authorization.requested"
		completion := p.snapshot["eventType"] == "heimdall.authorization.completed"
		if (completion && p.snapshot["outcome"] == "outcome_unknown") || (p.snapshot["eventType"] == "heimdall.recovery.completed" && p.snapshot["action"] == "authorization.recover") {
			return contract.ErrInvalidArgument
		}
		resolution := "ordinary"
		ids := []string{p.receipt.EventID}
		if request || completion {
			var requestID, state string
			e = tx.QueryRowContext(ctx, "SELECT request_event_id,resolution FROM heimdall_retention.decision_identities WHERE product_id=$1 AND environment=$2 AND decision_id=$3", a.product, a.environment, p.snapshot["decisionId"]).Scan(&requestID, &state)
			if e != nil && e != sql.ErrNoRows {
				return databaseError(ctx)
			}
			if request {
				if e == nil {
					return contract.ErrConflict
				}
				resolution = "pending"
			} else {
				if e == sql.ErrNoRows {
					return contract.ErrAuditUnavailable
				}
				if state != "pending" {
					return contract.ErrConflict
				}
				original, err := read(requestID)
				if err != nil {
					return err
				}
				if original == nil || original.State != "live" || !sameText(original.Identity.TenantID, tenant) {
					return contract.ErrAuditUnavailable
				}
				resolution = "completed"
				ids = []string{requestID, p.receipt.EventID}
			}
		}
		inserted, err := a.writePrepared(ctx, tx, p, false)
		if err != nil {
			return err
		}
		if !inserted {
			return contract.ErrAuditUnavailable
		}
		if e = a.finalizeCapacityEvent(ctx, tx, p.snapshot, inserted); e != nil {
			return e
		}
		if _, e = a.retainIdentityGroupTx(ctx, tx, tenant, resolution, ids); e != nil {
			return e
		}
	}
	return nil
}

func (a *Audit) lockRetainedCapacity(ctx context.Context, tx *sql.Tx) error {
	gate := lockRuntimeCapacity
	if a.retainedRuntime {
		gate = lockRetainedRuntime
	}
	if e := gate(ctx, tx); e != nil {
		return e
	}
	var initialized bool
	if e := tx.QueryRowContext(ctx, "SELECT singleton FROM heimdall_retention.capacity WHERE singleton FOR UPDATE").Scan(&initialized); e != nil {
		if e == sql.ErrNoRows {
			return contract.ErrAuditUnavailable
		}
		return databaseError(ctx)
	}
	return nil
}
func (a *Audit) readRetainedTx(ctx context.Context, tx *sql.Tx, id string) (*RetainedEventInspection, error) {
	var row retainedRow
	var identity, payload []byte
	if e := tx.QueryRowContext(ctx, a.retainedHistorySQL(eligibilityReadSQL), a.retainedHistoryArgs(id)...).Scan(&row.Ready, &row.Present, &row.Live, &row.Markers, &row.Tenant, &identity, &payload, &row.Valid); e != nil {
		return nil, databaseError(ctx)
	}
	row.Identity, row.Payload = identity, payload
	return a.decodeRetainedRow(row, nil, id, false)
}
