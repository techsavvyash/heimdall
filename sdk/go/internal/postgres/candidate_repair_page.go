package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type CandidateRepairRow struct {
	Principal string `json:"principal"`
	Position  string `json:"position"`
	Hash      string `json:"hash"`
}

func (o *OperationRecords) CandidateRepairPage(ctx context.Context, workspaceID, after string) ([]CandidateRepairRow, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b := o.binding
	statement, args := o.readQuery(`SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
       COALESCE(jsonb_agg(to_jsonb(c) ORDER BY c.position::bigint),'[]'::jsonb) AS candidates
       FROM (SELECT principal,position::text,encode(identity_hash,'hex') AS hash
       FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND environment=$2
       AND workspace_id=$3 AND position>$4::bigint ORDER BY membership_candidates.position LIMIT 21) c`,
		[]any{b.ProductID, b.Environment, workspaceID, after})
	var primary bool
	var raw []byte
	if err := o.db.QueryRowContext(ctx, statement, args...).Scan(&primary, &raw); err != nil {
		return nil, false, databaseError(ctx)
	}
	var rows []CandidateRepairRow
	if !primary || json.Unmarshal(raw, &rows) != nil || rows == nil || len(rows) > 21 {
		return nil, false, contract.ErrUnavailable
	}
	previous, _ := workspaceDecimal(&after, 9223372036854775807, false)
	for _, row := range rows {
		digest := sha256.Sum256([]byte(row.Principal))
		position, ok := workspaceDecimal(&row.Position, 9223372036854775807, true)
		if !ok || position <= previous || row.Hash != hex.EncodeToString(digest[:]) {
			return nil, false, contract.ErrUnavailable
		}
		previous = position
	}
	return rows[:min(len(rows), 20)], len(rows) > 20, nil
}
