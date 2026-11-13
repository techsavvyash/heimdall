package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// RetirementCursor is trusted maintenance state for one sweep.
type RetirementCursor struct {
	ProductID   string  `json:"productId"`
	Environment string  `json:"environment"`
	TenantID    *string `json:"tenantId"`
	Now         int64   `json:"now"`
	RetainForMS int64   `json:"retainForMs"`
	EventID     string  `json:"eventId"`
}

type RetirementPage struct {
	EventIDs []string          `json:"eventIds"`
	Next     *RetirementCursor `json:"next"`
}

var retirementPageSQL = `
WITH scanned AS MATERIALIZED (
 SELECT event_id FROM heimdall.audit_events
 WHERE product_id=$7 AND environment=$8 AND event_id>$9
 ORDER BY event_id LIMIT $10
)
SELECT admission.ready, (SELECT count(*)::int FROM scanned), (SELECT max(event_id) FROM scanned),
 EXISTS(SELECT 1 FROM scanned s LEFT JOIN heimdall_retention.event_identities i
 ON i.product_id=$7 AND i.environment=$8 AND i.event_id=s.event_id WHERE i.event_id IS NULL),
 coalesce((SELECT jsonb_agg(i.event_id ORDER BY i.event_id) FROM scanned s
 JOIN heimdall_retention.event_identities i ON i.product_id=$7 AND i.environment=$8 AND i.event_id=s.event_id
 WHERE i.tenant_id IS NOT DISTINCT FROM $11::text AND i.kind IN ('ordinary','request') AND i.recorded_at<=$12),'[]'::jsonb)
FROM (` + retentionReadinessSQL + `) admission`

// ScanRetirementCandidates returns root IDs and a cursor from one primary snapshot.
// A later authorized retirement must classify the group again under its write locks.
func (a *RetainedRuntimeAudit) ScanRetirementCandidates(ctx context.Context, tenant *string, now, retainForMS int64, after *RetirementCursor, limit int) (*RetirementPage, error) {
	if ctx == nil || (tenant != nil && !valid(*tenant, 128)) || now < 0 || now > maxSafe || retainForMS < 1 || retainForMS > maxSafe || limit < 1 || limit > 8 {
		return nil, contract.ErrInvalidArgument
	}
	if tenant != nil {
		value := *tenant
		tenant = &value
	}
	audit := a.audit
	last := ""
	if after != nil {
		if after.ProductID != audit.product || after.Environment != audit.environment || !sameText(after.TenantID, tenant) || after.Now != now || after.RetainForMS != retainForMS || !valid(after.EventID, 128) {
			return nil, contract.ErrInvalidArgument
		}
		last = after.EventID
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	tx, e := audit.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return nil, databaseError(ctx)
	}
	defer tx.Rollback()
	var ready, missingIdentity bool
	var scanned int
	var lastScanned *string
	var selectedRaw []byte
	args := append(retentionReadinessArgs(), audit.product, audit.environment, last, limit, tenant, now-retainForMS)
	if e = tx.QueryRowContext(ctx, retirementPageSQL, args...).Scan(&ready, &scanned, &lastScanned, &missingIdentity, &selectedRaw); e != nil {
		return nil, databaseError(ctx)
	}
	if !ready {
		return nil, contract.ErrUnavailable
	}
	var selected []string
	if missingIdentity || json.Unmarshal(selectedRaw, &selected) != nil || selected == nil || scanned < 0 || scanned > limit || len(selected) > scanned || (scanned > 0 && (lastScanned == nil || !valid(*lastScanned, 128))) {
		return nil, contract.ErrAuditUnavailable
	}
	page := &RetirementPage{EventIDs: make([]string, 0, len(selected))}
	for _, id := range selected {
		candidate, e := audit.readRetirementCandidate(ctx, tx, tenant, id, now, retainForMS)
		if e != nil {
			return nil, e
		}
		if candidate.reason == "" {
			return nil, contract.ErrAuditUnavailable
		}
		if candidate.reason == "candidate" {
			page.EventIDs = append(page.EventIDs, id)
		}
	}
	if scanned == limit {
		page.Next = &RetirementCursor{audit.product, audit.environment, tenant, now, retainForMS, *lastScanned}
	}
	if e = tx.Rollback(); e != nil {
		return nil, databaseError(ctx)
	}
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	return page, nil
}
