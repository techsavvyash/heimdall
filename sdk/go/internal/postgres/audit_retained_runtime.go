package postgres

import (
	"context"
	"database/sql"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"strings"
)

// RetainedRuntimeAudit exposes only retained operations under v4/v5 admission.
// Legacy Audit primitives remain available separately to offline migration tools.
type RetainedRuntimeAudit struct{ audit *Audit }

func NewRetainedRuntimeAudit(db *sql.DB, product, environment string, sinks []string, validate func(map[string]any) error) (*RetainedRuntimeAudit, error) {
	a, e := NewAudit(db, product, environment, sinks, validate)
	if e != nil {
		return nil, e
	}
	a.retainedRuntime = true
	return &RetainedRuntimeAudit{a}, nil
}
func (a *RetainedRuntimeAudit) Append(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
	return a.audit.AppendRetained(ctx, event)
}

// appendWith is restricted to SDK-local transactional mutations.
func (a *RetainedRuntimeAudit) appendWith(ctx context.Context, event map[string]any, mutate func(context.Context, *sql.Tx) error) (contract.AuditReceipt, error) {
	return a.audit.appendRetainedWith(ctx, event, mutate)
}
func (a *RetainedRuntimeAudit) Recover(ctx context.Context, terminal, administration map[string]any, cutoff int64) (*AuditRecoveryReceipt, error) {
	return a.audit.RecoverRetained(ctx, terminal, administration, cutoff)
}

// RecoverRuntime satisfies the bounded maintenance port using retained recovery.
func (a *RetainedRuntimeAudit) RecoverRuntime(ctx context.Context, terminal, administration map[string]any, cutoff int64) (*AuditRecoveryReceipt, error) {
	return a.Recover(ctx, terminal, administration, cutoff)
}
func (a *RetainedRuntimeAudit) ScanPendingDecisions(ctx context.Context, cutoff int64, after *AuditDecisionCursor, limit int) (*AuditDecisionPage, error) {
	return a.audit.ScanPendingDecisions(ctx, cutoff, after, limit)
}
func (a *RetainedRuntimeAudit) ReadResolution(ctx context.Context, id string) (*AuditResolution, error) {
	return a.audit.ReadRetainedResolution(ctx, id)
}
func (a *RetainedRuntimeAudit) Inspect(ctx context.Context, tenant *string, id string) (*RetainedEventInspection, error) {
	return a.audit.InspectRetainedEvent(ctx, tenant, id)
}
func (a *RetainedRuntimeAudit) ResolveReceipt(ctx context.Context, event map[string]any) (*contract.AuditReceipt, error) {
	return a.audit.ResolveRetainedReceipt(ctx, event)
}
func (a *RetainedRuntimeAudit) Eligibility(ctx context.Context, tenant *string, id string, now, duration int64) (string, error) {
	return a.audit.InspectRetentionEligibility(ctx, tenant, id, now, duration)
}

func (a *RetainedRuntimeAudit) Pending(ctx context.Context, sink string, now int64, limit int, tenant string) ([]PendingAudit, error) {
	return a.audit.pending(ctx, sink, now, limit, tenant)
}
func (a *RetainedRuntimeAudit) BeginAttempt(ctx context.Context, sink, id string, attempts int, now, retryAt int64) (*int, error) {
	return a.audit.BeginAttempt(ctx, sink, id, attempts, now, retryAt)
}
func (a *RetainedRuntimeAudit) DeferAttempt(ctx context.Context, sink, id string, attempt int, retryAt int64) (bool, error) {
	return a.audit.DeferAttempt(ctx, sink, id, attempt, retryAt)
}
func (a *RetainedRuntimeAudit) Quarantine(ctx context.Context, sink, id string, attempts int) (bool, error) {
	return a.audit.Quarantine(ctx, sink, id, attempts)
}
func (a *RetainedRuntimeAudit) RequeueQuarantined(ctx context.Context, sink, id string, attempts int, retryAt int64, tenant string) (bool, error) {
	return a.audit.requeueQuarantined(ctx, sink, id, attempts, retryAt, tenant)
}
func (a *RetainedRuntimeAudit) Acknowledge(ctx context.Context, batch, receipt contract.AuditDelivery, at int64) error {
	return a.audit.Acknowledge(ctx, batch, receipt, at)
}

var retainedDeliveryReadinessSQL = "SELECT ready AND current_setting('default_transaction_read_only')='off' FROM (" + retentionReadinessSQL + ") admission"

func lockRetainedDelivery(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock_shared(74162401)"); err != nil {
		return databaseError(ctx)
	}
	var ready bool
	if err := tx.QueryRowContext(ctx, retainedDeliveryReadinessSQL, retentionReadinessArgs()...).Scan(&ready); err != nil || !ready {
		return databaseError(ctx)
	}
	return nil
}

func lockRetainedRuntime(ctx context.Context, tx *sql.Tx) error {
	if e := lockRetentionAdmission(ctx, tx); e != nil {
		return e
	}
	return lockAuditCapacity(ctx, tx)
}
func lockRetentionAdmission(ctx context.Context, tx *sql.Tx) error {
	if _, e := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock_shared(74162401)"); e != nil {
		return databaseError(ctx)
	}
	var ready bool
	if e := tx.QueryRowContext(ctx, retentionReadinessSQL, retentionReadinessArgs()...).Scan(&ready); e != nil || !ready {
		return databaseError(ctx)
	}
	return nil
}

// Keep the shared history selectors' scope at $5..$7; v4 adds receipts at $8..$9.
func (a *Audit) retainedHistorySQL(query string) string {
	if !a.retainedRuntime {
		return query
	}
	ready := strings.NewReplacer("$5", "$8", "$6", "$9").Replace(retentionReadinessSQL)
	return strings.ReplaceAll(query, capacityReadinessSQL, ready)
}
func (a *Audit) retainedHistoryArgs(id string) []any {
	args := []any{sessionChecksum, decisionChecksum, capacityUpgradeChecksums[0], capacityUpgradeChecksums[1], a.product, a.environment, id}
	if a.retainedRuntime {
		args = append(args, pq.Array(retentionUpgradeChecksums[:]), retentionUpgradeChecksums[0])
	}
	return args
}

func retainedConnection(ctx context.Context, db *sql.DB, runtime bool) (*sql.Conn, error) {
	conn, e := db.Conn(ctx)
	if e != nil {
		return nil, databaseError(ctx)
	}
	if runtime {
		var primary bool
		if e = conn.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'").Scan(&primary); e != nil || !primary {
			conn.Close()
			return nil, databaseError(ctx)
		}
	}
	return conn, nil
}

// AppendIdentityWindow atomically retains a trusted collector page and its checkpoint.
func (a *RetainedRuntimeAudit) AppendIdentityWindow(ctx context.Context, events []map[string]any, advance func(context.Context, *sql.Tx) error) error {
	if ctx == nil || advance == nil || len(events) > 256 {
		return contract.ErrInvalidArgument
	}
	prepared := make([]*preparedAudit, 0, len(events))
	for _, event := range events {
		if event["source"] != "zitadel" || event["eventType"] != "heimdall.identity.observed" {
			return contract.ErrInvalidArgument
		}
		p, err := a.audit.prepare(event)
		if err != nil {
			return err
		}
		prepared = append(prepared, p)
	}
	return a.audit.retainedWriteTransaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := advance(ctx, tx); err != nil {
			return err
		}
		for _, event := range prepared {
			if err := a.audit.appendPreparedRetained(ctx, tx, event); err != nil {
				return err
			}
		}
		return nil
	})
}
