package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type operationAudit interface {
	appendWith(context.Context, map[string]any, func(context.Context, *sql.Tx) error) (contract.AuditReceipt, error)
}
type retainedManagedAudit struct{ audit *RetainedRuntimeAudit }

func (a retainedManagedAudit) appendWith(ctx context.Context, event map[string]any, mutate func(context.Context, *sql.Tx) error) (contract.AuditReceipt, error) {
	return a.audit.appendWith(ctx, event, func(ctx context.Context, tx *sql.Tx) error {
		var ready bool
		if e := tx.QueryRowContext(ctx, managedOnlyReadinessSQL, managedChecksum).Scan(&ready); e != nil || !ready {
			return databaseError(ctx)
		}
		return mutate(ctx, tx)
	})
}

// NewRetainedOperationRecords is the v4 runtime composition; no constructor I/O.
// The caller still authenticates, authorizes and establishes remote-write evidence.
func NewRetainedOperationRecords(db *sql.DB, b OperationBinding, sinks []string) (*OperationRecords, error) {
	o, e := NewOperationRecords(db, b, sinks)
	if e != nil {
		return nil, e
	}
	a, e := NewRetainedRuntimeAudit(db, b.ProductID, b.Environment, sinks, validateOperationAudit)
	if e != nil {
		return nil, e
	}
	o.audit = retainedManagedAudit{a}
	o.retainedRuntime = true
	return o, nil
}

func (o *OperationRecords) readQuery(statement string, args []any) (string, []any) {
	return managedReadQuery(statement, args, o.retainedRuntime)
}
