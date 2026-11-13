package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type OperationMetrics struct {
	Blocked    int64  `json:"blocked"`
	Attempts   int64  `json:"attempts"`
	QueueAgeMS *int64 `json:"queueAgeMs"`
}

func (o *OperationRecords) Metrics(ctx context.Context) (OperationMetrics, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	statement, args := o.readQuery(`SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
        count(*) FILTER (WHERE state IN ('dispatching','uncertain'))::text AS blocked,
        coalesce(sum(dispatch_count),0)::text AS attempts,
        min(created_at)::text AS oldest
       FROM heimdall_managed.operations WHERE product_id=$1 AND environment=$2
       AND state NOT IN ('completed','cancelled')`, []any{o.binding.ProductID, o.binding.Environment})
	var primary bool
	var blocked, attempts string
	var oldest sql.NullString
	if err := o.db.QueryRowContext(ctx, statement, args...).Scan(&primary, &blocked, &attempts, &oldest); err != nil {
		return OperationMetrics{}, databaseError(ctx)
	}
	b, bOK := workspaceDecimal(&blocked, maxSafe, false)
	a, aOK := workspaceDecimal(&attempts, maxSafe, false)
	if !primary || !bOK || !aOK {
		return OperationMetrics{}, contract.ErrUnavailable
	}
	result := OperationMetrics{Blocked: b, Attempts: a}
	if oldest.Valid {
		at, ok := workspaceDecimal(&oldest.String, maxSafe, false)
		if !ok {
			return OperationMetrics{}, contract.ErrUnavailable
		}
		age := max(0, time.Now().UnixMilli()-at)
		result.QueueAgeMS = &age
	}
	return result, contract.CheckCancellation(ctx)
}
