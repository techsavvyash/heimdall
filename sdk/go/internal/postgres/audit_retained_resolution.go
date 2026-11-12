package postgres

import (
	"context"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"strings"
	"time"
)

// ReadRetainedResolution returns nil only for an unknown scoped decision. Valid
// retired history returns not_found; missing or corrupt history is audit_unavailable.
func (a *Audit) ReadRetainedResolution(ctx context.Context, decisionID string) (*AuditResolution, error) {
	if ctx == nil || !valid(decisionID, 128) {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var rootRaw, membersRaw []byte
	var resolution *string
	var bound, held, retry, sourcePresent bool
	e := a.db.QueryRowContext(ctx, a.retainedHistorySQL(retainedResolutionSQL), a.retainedHistoryArgs(decisionID)...).Scan(&rootRaw, &resolution, &membersRaw, &bound, &held, &retry, &sourcePresent)
	if e != nil {
		return nil, databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var row retainedRow
	if json.Unmarshal(rootRaw, &row) != nil {
		return nil, contract.ErrAuditUnavailable
	}
	var identity contract.RetainedIdentity
	if row.Present && json.Unmarshal(row.Identity, &identity) != nil {
		return nil, contract.ErrAuditUnavailable
	}
	root, e := a.decodeRetainedRow(row, row.Tenant, identity.EventID, true)
	if e != nil {
		return nil, e
	}
	if root == nil {
		if sourcePresent {
			return nil, contract.ErrAuditUnavailable
		}
		return nil, nil
	}
	if resolution == nil || root.Identity.Kind != "request" || root.Identity.DecisionID == nil || *root.Identity.DecisionID != decisionID {
		return nil, contract.ErrAuditUnavailable
	}
	expected := map[string]int{"pending": 1, "completed": 2, "recovered": 3}[*resolution]
	var members []struct {
		EventID string      `json:"eventId"`
		Row     retainedRow `json:"row"`
	}
	if json.Unmarshal(membersRaw, &members) != nil || expected == 0 || len(members) != expected {
		return nil, contract.ErrAuditUnavailable
	}
	sources := []contract.RetentionSource{}
	result := &AuditResolution{DecisionID: decisionID, State: *resolution}
	seen := map[string]bool{}
	for _, m := range members {
		member, err := a.decodeRetainedRow(m.Row, row.Tenant, m.EventID, true)
		if err != nil {
			return nil, err
		}
		if member == nil || member.State != root.State || seen[m.EventID] || member.Identity.DecisionID == nil || *member.Identity.DecisionID != decisionID {
			return nil, contract.ErrAuditUnavailable
		}
		seen[m.EventID] = true
		if root.State == "live" {
			sources = append(sources, contract.RetentionSource{Event: member.Event, Digest: member.Identity.Digest, Routing: member.Identity.Routing, DeliveryRows: len(member.Identity.Routing)})
			switch member.Identity.Kind {
			case "request":
				result.Request = member.Event
			case "terminal":
				result.Terminal = member.Event
			case "recovery":
				result.Administration = member.Event
			default:
				return nil, contract.ErrAuditUnavailable
			}
		}
	}
	if root.State == "retired" {
		return nil, contract.ErrNotFound
	}
	if !bound {
		return nil, contract.ErrAuditUnavailable
	}
	if _, e = contract.BuildRetentionIdentities(contract.RetentionScope{ProductID: a.product, Environment: a.environment, TenantID: row.Tenant}, *resolution, sources); e != nil {
		return nil, e
	}
	return result, nil
}

// Resolve the request inside the same statement as every group member. The
// fallback source check prevents a missing compact decision becoming "absent".
var retainedResolutionSQL = `SELECT snapshot.*,
 EXISTS(SELECT 1 FROM heimdall.audit_decisions WHERE product_id=$5 AND environment=$6 AND decision_id=$7)
 OR EXISTS(SELECT 1 FROM heimdall_retention.event_identities WHERE product_id=$5 AND environment=$6 AND decision_id=$7)
 OR EXISTS(SELECT 1 FROM heimdall_retention.decision_identities WHERE product_id=$5 AND environment=$6 AND decision_id=$7) AS source_present
 FROM (` + strings.ReplaceAll(retentionEligibilitySQL, "$7", `(SELECT request_event_id FROM heimdall_retention.decision_identities WHERE product_id=$5 AND environment=$6 AND decision_id=$7)`) + `) snapshot`
