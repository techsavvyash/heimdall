package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type AuditDeliveryCapacity struct {
	Events, EventLimit, Bytes, ByteLimit int64
	Deliveries, DeliveryLimit            int64
	RetainedRows, RetainedRowLimit       int64
}

type AuditDeliveryMetrics struct {
	Pending, Due, Quarantined int64
	OldestPendingAgeMS        *int64
	Capacity                  AuditDeliveryCapacity
}

func (a *RetainedRuntimeAudit) DeliveryMetrics(ctx context.Context, sink string, now int64) (AuditDeliveryMetrics, error) {
	empty := AuditDeliveryMetrics{}
	if ctx == nil || !valid(sink, 128) || now < 0 || now > maxSafe {
		return empty, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return empty, err
	}
	tx, err := a.audit.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return empty, databaseError(ctx)
	}
	defer tx.Rollback()
	var ready bool
	if err := tx.QueryRowContext(ctx, retainedDeliveryReadinessSQL, retentionReadinessArgs()...).Scan(&ready); err != nil || !ready {
		return empty, databaseError(ctx)
	}
	var result AuditDeliveryMetrics
	var oldest sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE d.state='pending'),
 count(*) FILTER (WHERE d.state='pending' AND d.next_attempt_at<=$4),
 count(*) FILTER (WHERE d.state='quarantined'),
 min(e.recorded_at) FILTER (WHERE d.state='pending')
FROM heimdall.audit_deliveries d JOIN heimdall.audit_events e USING(product_id,environment,event_id)
WHERE d.product_id=$1 AND d.environment=$2 AND d.sink_id=$3 AND d.state IN ('pending','quarantined')`,
		a.audit.product, a.audit.environment, sink, now).Scan(&result.Pending, &result.Due, &result.Quarantined, &oldest)
	if err != nil {
		return empty, databaseError(ctx)
	}
	if oldest.Valid {
		age := max(int64(0), now-oldest.Int64)
		result.OldestPendingAgeMS = &age
	}
	c := &result.Capacity
	err = tx.QueryRowContext(ctx, `SELECT used_events+reserved_events,event_limit,used_bytes+reserved_bytes,p.byte_limit,
 used_deliveries+reserved_deliveries,delivery_limit,r.used_rows+r.reserved_rows,LEAST(r.row_limit,r.byte_limit/8192)
FROM heimdall_capacity.policy p CROSS JOIN heimdall_retention.capacity r
WHERE p.singleton AND r.singleton`).Scan(&c.Events, &c.EventLimit, &c.Bytes, &c.ByteLimit, &c.Deliveries, &c.DeliveryLimit, &c.RetainedRows, &c.RetainedRowLimit)
	if err != nil {
		return empty, databaseError(ctx)
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return empty, err
	}
	return result, nil
}
