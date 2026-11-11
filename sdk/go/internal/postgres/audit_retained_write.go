package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

// AppendRetained is a prepared compatible writer, not public v4 activation.
// New recovery pairs use RecoverRetained to preserve atomic administration.
func (a *Audit) AppendRetained(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
	empty := contract.AuditReceipt{}
	if ctx == nil {
		return empty, contract.ErrInvalidArgument
	}
	p, e := a.prepare(event)
	if e != nil {
		return empty, e
	}
	var tenant *string
	if v, ok := p.snapshot["tenantId"].(string); ok {
		tenant = &v
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e = contract.CheckCancellation(ctx); e != nil {
		return empty, e
	}
	tx, e := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return empty, databaseError(ctx)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SET LOCAL synchronous_commit='on'"); e != nil {
		return empty, databaseError(ctx)
	}
	if e = lockRetainedCapacity(ctx, tx); e != nil {
		return empty, e
	}
	read := func(id string) (*RetainedEventInspection, error) { return a.readRetainedTx(ctx, tx, id) }
	existing, e := read(p.receipt.EventID)
	if e != nil {
		return empty, e
	}
	if existing != nil {
		if existing.Identity.Digest != p.receipt.PayloadDigest || existing.Identity.RecordedAt != p.receipt.RecordedAt {
			return empty, contract.ErrConflict
		}
	} else {
		request := p.snapshot["eventType"] == "heimdall.authorization.requested"
		completion := p.snapshot["eventType"] == "heimdall.authorization.completed"
		if (completion && p.snapshot["outcome"] == "outcome_unknown") || (p.snapshot["eventType"] == "heimdall.recovery.completed" && p.snapshot["action"] == "authorization.recover") {
			return empty, contract.ErrInvalidArgument
		}
		resolution := "ordinary"
		ids := []string{p.receipt.EventID}
		if request || completion {
			var requestID, state string
			e = tx.QueryRowContext(ctx, "SELECT request_event_id,resolution FROM heimdall_retention.decision_identities WHERE product_id=$1 AND environment=$2 AND decision_id=$3", a.product, a.environment, p.snapshot["decisionId"]).Scan(&requestID, &state)
			if e != nil && e != sql.ErrNoRows {
				return empty, databaseError(ctx)
			}
			if request {
				if e == nil {
					return empty, contract.ErrConflict
				}
				resolution = "pending"
			} else {
				if e == sql.ErrNoRows {
					return empty, contract.ErrAuditUnavailable
				}
				if state != "pending" {
					return empty, contract.ErrConflict
				}
				original, err := read(requestID)
				if err != nil {
					return empty, err
				}
				if original == nil || original.State != "live" || !sameText(original.Identity.TenantID, tenant) {
					return empty, contract.ErrAuditUnavailable
				}
				resolution = "completed"
				ids = []string{requestID, p.receipt.EventID}
			}
		}
		inserted, err := a.writePrepared(ctx, tx, p, false)
		if err != nil {
			return empty, err
		}
		if !inserted {
			return empty, contract.ErrAuditUnavailable
		}
		if e = a.finalizeCapacityEvent(ctx, tx, p.snapshot, inserted); e != nil {
			return empty, e
		}
		if _, e = a.retainIdentityGroupTx(ctx, tx, tenant, resolution, ids); e != nil {
			return empty, e
		}
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return empty, e
	}
	if e = tx.Commit(); e != nil {
		return empty, contract.ErrOutcomeUnknown
	}
	return p.receipt, nil
}

func lockRetainedCapacity(ctx context.Context, tx *sql.Tx) error {
	if e := lockRuntimeCapacity(ctx, tx); e != nil {
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
	if e := tx.QueryRowContext(ctx, retainedReadSQL, sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1], a.product, a.environment, id).Scan(&row.Ready, &row.Present, &row.Live, &row.Markers, &row.Tenant, &identity, &payload, &row.Valid); e != nil {
		return nil, databaseError(ctx)
	}
	row.Identity, row.Payload = identity, payload
	return a.decodeRetainedRow(row, nil, id, false)
}
