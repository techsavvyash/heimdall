package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"strconv"
	"time"
	"unicode/utf8"
)

type ManagedWorkspace struct {
	contract.WorkspaceRecord
	SchemaVersion  int
	Name, Revision string
}
type WorkspaceRecords struct {
	db                   *sql.DB
	product, environment string
}

// NewWorkspaceRecords owns neither the pool nor schema lifecycle.
func NewWorkspaceRecords(db *sql.DB, product, environment string) (*WorkspaceRecords, error) {
	if db == nil || !valid(product, 64) || !valid(environment, 64) {
		return nil, contract.ErrInvalidArgument
	}
	return &WorkspaceRecords{db, product, environment}, nil
}

type workspaceRow struct {
	Primary     bool    `json:"primary"`
	Product     *string `json:"product_id"`
	Environment *string `json:"environment"`
	Workspace   *string `json:"workspace_id"`
	Name        *string `json:"name"`
	Lifecycle   *string `json:"lifecycle"`
	Blocked     *bool   `json:"mutation_blocked"`
	Revision    *string `json:"revision"`
	Created     *string `json:"created_at"`
	Updated     *string `json:"updated_at"`
}

func workspaceDecimal(s *string, max int64, positive bool) (int64, bool) {
	if s == nil {
		return 0, false
	}
	n, e := strconv.ParseInt(*s, 10, 64)
	return n, e == nil && n >= 0 && n <= max && (!positive || n > 0) && strconv.FormatInt(n, 10) == *s
}
func decodeWorkspace(r workspaceRow, scope contract.WorkspaceScope) (*ManagedWorkspace, error) {
	if !r.Primary {
		return nil, contract.ErrUnavailable
	}
	if r.Workspace == nil {
		if r.Product != nil || r.Environment != nil || r.Name != nil || r.Lifecycle != nil || r.Blocked != nil || r.Revision != nil || r.Created != nil || r.Updated != nil {
			return nil, contract.ErrUnavailable
		}
		return nil, nil
	}
	created, c := workspaceDecimal(r.Created, maxSafe, false)
	updated, u := workspaceDecimal(r.Updated, maxSafe, false)
	_, v := workspaceDecimal(r.Revision, 9223372036854775807, true)
	if r.Product == nil || *r.Product != scope.ProductID || r.Environment == nil || *r.Environment != scope.Environment || *r.Workspace != scope.WorkspaceID || r.Name == nil || !utf8.ValidString(*r.Name) || utf8.RuneCountInString(*r.Name) < 1 || utf8.RuneCountInString(*r.Name) > 256 || len(*r.Name) > 1024 || r.Lifecycle == nil || r.Blocked == nil || !c || !u || !v || created > updated {
		return nil, contract.ErrUnavailable
	}
	switch *r.Lifecycle {
	case "pending", "active", "suspended", "tombstoned":
	default:
		return nil, contract.ErrUnavailable
	}
	return &ManagedWorkspace{WorkspaceRecord: contract.WorkspaceRecord{WorkspaceScope: scope, Lifecycle: *r.Lifecycle, MutationBlocked: *r.Blocked, CreatedAt: created, UpdatedAt: updated}, SchemaVersion: 1, Name: *r.Name, Revision: *r.Revision}, nil
}

// Get reads the current primary snapshot. Missing is distinct from failed/corrupt.
func (w *WorkspaceRecords) Get(ctx context.Context, id string) (*ManagedWorkspace, error) {
	if ctx == nil || !valid(id, 128) {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var r workspaceRow
	e := w.db.QueryRowContext(ctx, `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
 w.product_id,w.environment,w.workspace_id,w.name,w.lifecycle,w.mutation_blocked,w.revision::text,w.created_at::text,w.updated_at::text
 FROM (SELECT 1) seed LEFT JOIN heimdall_managed.workspaces w ON w.product_id=$1 AND w.environment=$2 AND w.workspace_id=$3`, w.product, w.environment, id).Scan(&r.Primary, &r.Product, &r.Environment, &r.Workspace, &r.Name, &r.Lifecycle, &r.Blocked, &r.Revision, &r.Created, &r.Updated)
	if e != nil {
		return nil, databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	return decodeWorkspace(r, contract.WorkspaceScope{ProductID: w.product, Environment: w.environment, WorkspaceID: id})
}
