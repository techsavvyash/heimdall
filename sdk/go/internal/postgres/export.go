package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"regexp"
	"slices"
	"time"
)

var exportHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func decodeExportRow(row map[string]any, kind, product, environment string, workspace *string) (map[string]any, error) {
	bad := contract.ErrUnavailable
	keys := []string{"productId", "environment", "workspaceId", "createdAt", "updatedAt"}
	if kind == "workspaces" {
		keys = append(keys, "lifecycle", "mutationBlocked", "revision")
	} else {
		keys = append(keys, "operationId", "kind", "state", "dispatchCount", "generation", "nextAttemptAt", "storeId", "modelId", "modelHash", "manifestVersion")
	}
	str := func(key string) string { s, _ := row[key].(string); return s }
	if len(row) != len(keys) || str("productId") != product || str("environment") != environment || !valid(str("workspaceId"), 128) || (workspace != nil && str("workspaceId") != *workspace) {
		return nil, bad
	}
	out := map[string]any{}
	for _, key := range keys {
		v, ok := row[key]
		if !ok {
			return nil, bad
		}
		if slices.Contains([]string{"createdAt", "updatedAt", "dispatchCount", "generation", "nextAttemptAt", "manifestVersion"}, key) {
			text, ok := v.(string)
			if !ok {
				return nil, bad
			}
			n, ok := workspaceDecimal(&text, maxSafe, key == "generation" || key == "manifestVersion")
			if !ok || (key == "manifestVersion" && n > 2147483647) {
				return nil, bad
			}
			out[key] = n
		} else if key == "mutationBlocked" {
			b, ok := v.(bool)
			if !ok {
				return nil, bad
			}
			out[key] = b
		} else {
			s, ok := v.(string)
			if !ok {
				return nil, bad
			}
			out[key] = s
		}
	}
	if out["createdAt"].(int64) > out["updatedAt"].(int64) {
		return nil, bad
	}
	if kind == "workspaces" {
		revision := str("revision")
		_, ok := workspaceDecimal(&revision, 9223372036854775807, true)
		if !ok || !slices.Contains([]string{"pending", "active", "suspended", "tombstoned"}, str("lifecycle")) {
			return nil, bad
		}
	} else {
		if !valid(str("operationId"), 128) || !valid(str("storeId"), 128) || !valid(str("modelId"), 128) || !exportHash.MatchString(str("modelHash")) || !slices.Contains([]string{"create_workspace", "add_member", "remove_member", "assign_roles", "transfer_owner"}, str("kind")) || !slices.Contains([]string{"pending", "retryable", "dispatching", "confirmed", "uncertain", "completed", "cancelled"}, str("state")) {
			return nil, bad
		}
		count := out["dispatchCount"].(int64)
		if (slices.Contains([]string{"dispatching", "confirmed", "uncertain", "completed"}, str("state")) && count < 1) || (str("state") == "cancelled" && count != 0) {
			return nil, bad
		}
	}
	return out, nil
}

// ManagedExport is a privileged diagnostic port. Authorization and export audit belong to its caller.
type ManagedExport struct {
	retainedRuntime      bool
	db                   *sql.DB
	product, environment string
}

// Legacy schema port; runtime exports use NewRetainedManagedExport.
func NewManagedExport(db *sql.DB, product, environment string) (*ManagedExport, error) {
	if db == nil || !valid(product, 64) || !valid(environment, 64) {
		return nil, contract.ErrInvalidArgument
	}
	return &ManagedExport{db: db, product: product, environment: environment}, nil
}

// NewRetainedManagedExport requires compatible audit-v4 and managed-v1 snapshots.
func NewRetainedManagedExport(db *sql.DB, product, environment string) (*ManagedExport, error) {
	store, e := NewManagedExport(db, product, environment)
	if e != nil {
		return nil, e
	}
	store.retainedRuntime = true
	return store, nil
}

// Scan returns a bounded portable JSON page, never a restorable journal or permission to replay.
func (e *ManagedExport) Scan(ctx context.Context, kind string, workspace, after *string, limit int) ([]byte, error) {
	return e.scan(ctx, e.db, kind, workspace, after, limit)
}

// WithSnapshot scopes the export reader to one bounded primary transaction.
func (e *ManagedExport) WithSnapshot(ctx context.Context, read func(context.Context, contract.ManagedExportScan) error) error {
	if ctx == nil || read == nil || !e.retainedRuntime {
		return contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return databaseError(ctx)
	}
	defer tx.Rollback()
	err = read(ctx, func(ctx context.Context, kind string, workspace, after *string, limit int) ([]byte, error) {
		return e.scan(ctx, tx, kind, workspace, after, limit)
	})
	if err != nil {
		return err
	}
	if err = tx.Rollback(); err != nil {
		return databaseError(ctx)
	}
	return contract.CheckCancellation(ctx)
}

func (e *ManagedExport) scan(ctx context.Context, reader retentionReader, kind string, workspace, after *string, limit int) ([]byte, error) {
	if ctx == nil || (kind != "workspaces" && kind != "operations") || (workspace != nil && !valid(*workspace, 128)) || (kind == "operations" && workspace == nil) || (after != nil && !valid(*after, 128)) || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidArgument
	}
	// Own cursor/filter values before any connection wait.
	if workspace != nil {
		value := *workspace
		workspace = &value
	}
	if after != nil {
		value := *after
		after = &value
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	id := "workspace_id"
	fields := "'lifecycle',lifecycle,'mutationBlocked',mutation_blocked,'revision',revision::text"
	if kind == "operations" {
		id = "operation_id"
		fields = "'operationId',operation_id,'kind',kind,'state',state,'dispatchCount',dispatch_count::text,'generation',generation::text,'nextAttemptAt',next_attempt_at::text,'storeId',store_id,'modelId',model_id,'modelHash',encode(model_hash,'hex'),'manifestVersion',manifest_version::text"
	}
	var primary bool
	var raw []byte
	statement := `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
 COALESCE(jsonb_agg(record ORDER BY id COLLATE "C"),'[]'::jsonb) AS records FROM (
 SELECT ` + id + ` AS id,jsonb_build_object('productId',product_id,'environment',environment,'workspaceId',workspace_id,'createdAt',created_at::text,'updatedAt',updated_at::text,` + fields + `) AS record
 FROM heimdall_managed.` + kind + ` WHERE product_id=$1 AND environment=$2 AND ($3::text IS NULL OR workspace_id=$3)
 AND ($4::text IS NULL OR ` + id + ` COLLATE "C">$4 COLLATE "C") ORDER BY ` + id + ` COLLATE "C" LIMIT $5) page`
	statement, args := managedReadQuery(statement, []any{e.product, e.environment, workspace, after, limit + 1}, e.retainedRuntime)
	err := reader.QueryRowContext(ctx, statement, args...).Scan(&primary, &raw)
	if err != nil {
		return nil, databaseError(ctx)
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	var records []map[string]any
	if !primary || json.Unmarshal(raw, &records) != nil || records == nil || len(records) > limit+1 {
		return nil, contract.ErrUnavailable
	}
	previous := ""
	if after != nil {
		previous = *after
	}
	idKey := "workspaceId"
	if kind == "operations" {
		idKey = "operationId"
	}
	items := make([]map[string]any, 0, len(records))
	for _, row := range records {
		item, err := decodeExportRow(row, kind, e.product, e.environment, workspace)
		if err != nil {
			return nil, err
		}
		current := item[idKey].(string)
		if current <= previous {
			return nil, contract.ErrUnavailable
		}
		previous = current
		items = append(items, item)
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1][idKey]
	}
	return json.Marshal(map[string]any{"schemaVersion": 1, "productId": e.product, "environment": e.environment, "kind": kind, "workspaceId": workspace, "items": items, "nextId": next})
}
