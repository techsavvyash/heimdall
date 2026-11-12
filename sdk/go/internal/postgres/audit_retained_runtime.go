package postgres

import (
	"context"
	"database/sql"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"strings"
)

// RetainedRuntimeAudit exposes only retained operations under strict v4 admission.
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
