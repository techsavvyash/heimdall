package postgres

import (
	"context"
	"encoding/json"
	"github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

type OperationPage struct {
	Items           []*ManagedOperation
	NextOperationID *string
}

// Scan is a trusted maintenance read, not a work claim or permission to replay.
func (o *OperationRecords) Scan(ctx context.Context, queue string, after *string, now int64, limit int) (*OperationPage, error) {
	states := map[string][]string{"runnable": {"pending", "retryable"}, "recovery": {"dispatching", "uncertain"}, "completion": {"confirmed"},
		"reconcile": {"pending", "retryable", "dispatching", "uncertain", "confirmed"}}
	selectedStates, ok := states[queue]
	if ctx == nil || !ok || (after != nil && !valid(*after, 128)) || now < 0 || now > maxSafe || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var position any
	previous := ""
	if after != nil {
		previous = *after
		position = previous
	}
	var primary bool
	var raw []byte
	var count int
	statement := `WITH selected AS MATERIALIZED (
   SELECT * FROM heimdall_managed.operations WHERE product_id=$1 AND environment=$2 AND state=ANY($3::text[])
    AND ($4::text IS NULL OR operation_id COLLATE "C">$4 COLLATE "C") AND (NOT $5::boolean OR state NOT IN ('pending','retryable') OR next_attempt_at<=$6)
    ORDER BY operation_id COLLATE "C" LIMIT $7
  ), encoded AS MATERIALIZED (
   SELECT o.operation_id, ` + operationColumns + ` AS record FROM selected o
  ), sized AS (
   SELECT *,sum(octet_length(record::text)) OVER(ORDER BY operation_id COLLATE "C") AS size FROM encoded
  ) SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
   COALESCE(jsonb_agg(o.record ORDER BY o.operation_id COLLATE "C"),'[]'::jsonb),
   (SELECT count(*)::int FROM selected) FROM sized o WHERE size<=4194304`
	statement, args := o.readQuery(statement, []any{o.binding.ProductID, o.binding.Environment, pq.Array(selectedStates), position, queue == "runnable" || queue == "reconcile", now, limit + 1})
	e := o.db.QueryRowContext(ctx, statement, args...).Scan(&primary, &raw, &count)
	if e != nil {
		return nil, databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var rows []operationRow
	if !primary || json.Unmarshal(raw, &rows) != nil || rows == nil || count < len(rows) || count > limit+1 || (count > 0 && len(rows) == 0) {
		return nil, contract.ErrUnavailable
	}
	items := make([]*ManagedOperation, 0, len(rows))
	for _, row := range rows {
		r, err := decodeOperation(row, o.binding, row.ActorHash)
		if err != nil {
			return nil, err
		}
		stateOK := false
		for _, state := range selectedStates {
			if r.State == state {
				stateOK = true
			}
		}
		if r.OperationID <= previous || !stateOK || ((queue == "runnable" || queue == "reconcile") && (r.State == "pending" || r.State == "retryable") && r.NextAttemptAt > now) {
			return nil, contract.ErrUnavailable
		}
		previous = r.OperationID
		items = append(items, r)
	}
	more := count > limit || count > len(items)
	if len(items) > limit {
		items = items[:limit]
	}
	var next *string
	if more {
		id := items[len(items)-1].OperationID
		next = &id
	}
	return &OperationPage{items, next}, nil
}
