package auditflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type RetirementSweep struct {
	ID       string                     `json:"id"`
	TenantID *string                    `json:"tenantId"`
	At       int64                      `json:"at"`
	After    *contract.RetirementCursor `json:"after"`
}

type RetirementBatch struct {
	ProductID       string                     `json:"productId"`
	Environment     string                     `json:"environment"`
	PolicyVersion   string                     `json:"policyVersion"`
	ModelID         string                     `json:"modelId"`
	ManifestVersion int                        `json:"manifestVersion"`
	RetainForMS     int64                      `json:"retainForMs"`
	Sweep           RetirementSweep            `json:"sweep"`
	Operations      []MaintenanceOperation     `json:"operations"`
	Next            *contract.RetirementCursor `json:"next"`
}

// QueueRetirementPage scans one page and asks the product to atomically save
// the proposals and compare-and-set the sweep's cursor.
func (c *MaintenanceCommand) QueueRetirementPage(ctx context.Context, sweep RetirementSweep, limit int,
	scan func(context.Context, *string, int64, int64, *contract.RetirementCursor, int) (*contract.RetirementPage, error),
	save func(context.Context, RetirementBatch) (bool, error),
) error {
	const maxSafe = 9007199254740991
	if c == nil || c.auth == nil || ctx == nil || scan == nil || save == nil ||
		!maintenanceToken.MatchString(sweep.ID) || (sweep.TenantID != nil && !maintenanceToken.MatchString(*sweep.TenantID)) ||
		sweep.At < 0 || sweep.At > maxSafe || sweep.At > c.auth.writer.now().UnixMilli() || limit < 1 || limit > 8 {
		return contract.ErrInvalidArgument
	}
	if sweep.TenantID != nil {
		value := *sweep.TenantID
		sweep.TenantID = &value
	}
	if sweep.After != nil {
		value := *sweep.After
		if !c.matchesRetirementCursor(sweep, &value) {
			return contract.ErrInvalidArgument
		}
		value.TenantID = sweep.TenantID
		sweep.After = &value
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	var scanTenant *string
	if sweep.TenantID != nil {
		value := *sweep.TenantID
		scanTenant = &value
	}
	var scanAfter *contract.RetirementCursor
	if sweep.After != nil {
		value := *sweep.After
		value.TenantID = scanTenant
		scanAfter = &value
	}
	page, err := scan(ctx, scanTenant, sweep.At, c.retainForMS, scanAfter, limit)
	if err != nil {
		return err
	}
	if page == nil || len(page.EventIDs) > limit ||
		(page.Next != nil && (!c.matchesRetirementCursor(sweep, page.Next) ||
			(sweep.After != nil && page.Next.EventID == sweep.After.EventID))) {
		return contract.ErrAuditUnavailable
	}
	batch := RetirementBatch{
		ProductID: c.auth.writer.product, Environment: c.auth.writer.environment, PolicyVersion: c.auth.policy,
		ModelID: c.modelID, ManifestVersion: c.manifestVersion, RetainForMS: c.retainForMS,
		Sweep: sweep, Operations: make([]MaintenanceOperation, 0, len(page.EventIDs)), Next: page.Next,
	}
	seen := make(map[string]bool, len(page.EventIDs))
	for _, id := range page.EventIDs {
		if !maintenanceToken.MatchString(id) || seen[id] {
			return contract.ErrAuditUnavailable
		}
		seen[id] = true
		batch.Operations = append(batch.Operations, MaintenanceOperation{
			TenantID: sweep.TenantID, Action: "audit.retention.retire", TargetEventID: id,
			OperationID: batch.operationID(id), At: sweep.At,
		})
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	saved, err := save(ctx, batch)
	if err != nil {
		return err
	}
	if !saved {
		return contract.ErrConflict
	}
	return nil
}

func (batch RetirementBatch) operationID(target string) string {
	tenant := ""
	if batch.Sweep.TenantID != nil {
		tenant = *batch.Sweep.TenantID
	}
	fields := []string{"heimdall.retirement-proposal.v1", batch.ProductID, batch.Environment, batch.PolicyVersion,
		batch.ModelID, strconv.Itoa(batch.ManifestVersion), tenant, batch.Sweep.ID,
		strconv.FormatInt(batch.Sweep.At, 10), strconv.FormatInt(batch.RetainForMS, 10), target}
	digest := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return "sweep-" + hex.EncodeToString(digest[:])
}

func (c *MaintenanceCommand) matchesRetirementCursor(sweep RetirementSweep, cursor *contract.RetirementCursor) bool {
	return cursor.ProductID == c.auth.writer.product && cursor.Environment == c.auth.writer.environment &&
		tenantEqual(cursor.TenantID, sweep.TenantID) && cursor.Now == sweep.At && cursor.RetainForMS == c.retainForMS &&
		maintenanceToken.MatchString(cursor.EventID)
}

func tenantEqual(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// RunQueuedRetirement reauthorizes one persisted proposal under its pinned configuration.
func (c *MaintenanceCommand) RunQueuedRetirement(ctx context.Context, bearer string, batch RetirementBatch, index int) (contract.AuditReceipt, error) {
	if c == nil || c.auth == nil || index < 0 || index >= len(batch.Operations) {
		return contract.AuditReceipt{}, contract.ErrInvalidArgument
	}
	op := batch.Operations[index]
	if op.Action != "audit.retention.retire" || !tenantEqual(op.TenantID, batch.Sweep.TenantID) || op.At != batch.Sweep.At || op.OperationID != batch.operationID(op.TargetEventID) ||
		batch.ProductID != c.auth.writer.product || batch.Environment != c.auth.writer.environment || batch.PolicyVersion != c.auth.policy ||
		batch.ModelID != c.modelID || batch.ManifestVersion != c.manifestVersion || batch.RetainForMS != c.retainForMS {
		return contract.AuditReceipt{}, contract.ErrConflict
	}
	return c.Run(ctx, bearer, op)
}
