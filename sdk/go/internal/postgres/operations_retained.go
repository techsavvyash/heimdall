package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"regexp"
	"strconv"
	"strings"
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

var managedReadParameter = regexp.MustCompile(`\$[1-7]`)

func (o *OperationRecords) readQuery(statement string, args []any) (string, []any) {
	if !o.retainedRuntime {
		return statement, args
	}
	readiness := managedReadParameter.ReplaceAllStringFunc(managedRetentionReadinessSQL, func(p string) string {
		n, _ := strconv.Atoi(p[1:])
		return "$" + strconv.Itoa(n+len(args))
	})
	statement = strings.ReplaceAll(statement, "NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'", "("+readiness+")")
	args = append(args, retentionReadinessArgs()...)
	return statement, append(args, managedChecksum)
}
