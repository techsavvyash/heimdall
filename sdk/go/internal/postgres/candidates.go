package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

type Candidate struct {
	Position, Revision string
	ObservedAt         int64
	Workspace          ManagedWorkspace
}
type CandidatePage struct {
	Items        []Candidate
	NextPosition *string
}
type candidateRow struct {
	IdentityHash, Principal, Position, Revision, ObservedAt string
	Workspace                                               workspaceRow
}
type candidatePageRow struct {
	Primary bool
	Records []candidateRow
}
type CandidateRecords struct {
	retainedRuntime      bool
	db                   *sql.DB
	product, environment string
}

// Legacy schema constructor; runtime callers use NewRetainedCandidateRecords.
func NewCandidateRecords(db *sql.DB, product, environment string) (*CandidateRecords, error) {
	if db == nil || !valid(product, 64) || !valid(environment, 64) {
		return nil, contract.ErrInvalidArgument
	}
	return &CandidateRecords{db: db, product: product, environment: environment}, nil
}

// NewRetainedCandidateRecords reads only compatible audit-v4 and managed-v1 snapshots.
func NewRetainedCandidateRecords(db *sql.DB, product, environment string) (*CandidateRecords, error) {
	store, e := NewCandidateRecords(db, product, environment)
	if e != nil {
		return nil, e
	}
	store.retainedRuntime = true
	return store, nil
}

func decodeCandidatePage(value candidatePageRow, product, environment, principal, identityHash, after string, limit int) (*CandidatePage, error) {
	previous, ok := workspaceDecimal(&after, 9223372036854775807, false)
	if !ok || !value.Primary || value.Records == nil || len(value.Records) > limit+1 {
		return nil, contract.ErrUnavailable
	}
	items := make([]Candidate, 0, len(value.Records))
	seen := make(map[string]bool)
	for _, r := range value.Records {
		position, p := workspaceDecimal(&r.Position, 9223372036854775807, true)
		_, v := workspaceDecimal(&r.Revision, 9223372036854775807, true)
		observed, o := workspaceDecimal(&r.ObservedAt, maxSafe, false)
		if !p || !v || !o || position <= previous || r.IdentityHash != identityHash || r.Principal != principal || r.Workspace.Workspace == nil || !valid(*r.Workspace.Workspace, 128) {
			return nil, contract.ErrUnavailable
		}
		w, e := decodeWorkspace(r.Workspace, contract.WorkspaceScope{ProductID: product, Environment: environment, WorkspaceID: *r.Workspace.Workspace})
		if e != nil || w == nil || w.Lifecycle != "active" || w.MutationBlocked {
			return nil, contract.ErrUnavailable
		}
		if seen[w.WorkspaceID] {
			return nil, contract.ErrUnavailable
		}
		seen[w.WorkspaceID] = true
		items = append(items, Candidate{r.Position, r.Revision, observed, *w})
		previous = position
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		position := items[len(items)-1].Position
		next = &position
	}
	return &CandidatePage{items, next}, nil
}

// Scan returns hints from a primary snapshot, never verified membership or a signed cursor.
func (c *CandidateRecords) Scan(ctx context.Context, actor contract.Identity, after string, limit int) (*CandidatePage, error) {
	_, validPosition := workspaceDecimal(&after, 9223372036854775807, false)
	if ctx == nil || !validPosition || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidArgument
	}
	principal, e := contract.EncodeIdentity(actor.Issuer, actor.Subject)
	if e != nil {
		return nil, e
	}
	hash, _ := identityBinding(actor.Issuer, actor.Subject)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var row candidatePageRow
	var raw []byte
	statement := `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
  COALESCE(jsonb_agg(jsonb_build_object('identityHash',encode(p.identity_hash,'hex'),'principal',p.principal,'position',p.position::text,
   'revision',p.candidate_revision::text,'observedAt',p.observed_at::text,'workspace',jsonb_build_object('primary',true,
   'product_id',p.product_id,'environment',p.environment,'workspace_id',p.workspace_id,'name',p.name,'lifecycle',p.lifecycle,
   'mutation_blocked',p.mutation_blocked,'revision',p.revision::text,'created_at',p.created_at::text,'updated_at',p.updated_at::text)) ORDER BY p.position),'[]'::jsonb) AS records
  FROM (SELECT c.identity_hash,c.principal,c.position,c.revision AS candidate_revision,c.observed_at,w.*
   FROM heimdall_managed.membership_candidates c JOIN heimdall_managed.workspaces w USING(product_id,environment,workspace_id)
   WHERE c.product_id=$1 AND c.environment=$2 AND c.identity_hash=$3 AND c.position>$4::bigint
    AND w.lifecycle='active' AND NOT w.mutation_blocked ORDER BY c.position LIMIT $5) p`
	statement, args := managedReadQuery(statement, []any{c.product, c.environment, hash, after, limit + 1}, c.retainedRuntime)
	e = c.db.QueryRowContext(ctx, statement, args...).Scan(&row.Primary, &raw)
	if e != nil {
		return nil, databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if json.Unmarshal(raw, &row.Records) != nil {
		return nil, contract.ErrUnavailable
	}
	return decodeCandidatePage(row, c.product, c.environment, principal, hex.EncodeToString(hash), after, limit)
}
