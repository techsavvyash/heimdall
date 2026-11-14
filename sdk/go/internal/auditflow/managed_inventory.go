package auditflow

import (
	"context"
	"encoding/json"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// RunManagedInventory releases a complete bounded inventory from one snapshot.
func (a *Authentication) RunManagedInventory(
	ctx context.Context, raw string, request ManagedExportRequest, modelID string, manifestVersion int,
	policy func(context.Context, MaintenanceContext) (bool, error),
	snapshot func(context.Context, func(context.Context, contract.ManagedExportScan) error) error,
) ([]byte, error) {
	if request.After != nil || snapshot == nil {
		return nil, contract.ErrInvalidArgument
	}
	return a.RunManagedExport(ctx, raw, request, modelID, manifestVersion, policy,
		func(ctx context.Context, kind string, workspace, _ *string, limit int) ([]byte, error) {
			var inventory []byte
			err := snapshot(ctx, func(ctx context.Context, scan contract.ManagedExportScan) error {
				var after *string
				items := make([]json.RawMessage, 0)
				size := 0
				for range 100 {
					page, err := scan(ctx, kind, workspace, after, limit)
					if err != nil {
						return err
					}
					size += len(page)
					if size > 4*1024*1024 {
						return contract.ErrUnavailable
					}
					var envelope struct {
						SchemaVersion int               `json:"schemaVersion"`
						ProductID     string            `json:"productId"`
						Environment   string            `json:"environment"`
						Kind          string            `json:"kind"`
						WorkspaceID   json.RawMessage   `json:"workspaceId"`
						Items         []json.RawMessage `json:"items"`
						NextID        json.RawMessage   `json:"nextId"`
					}
					tenant, _ := json.Marshal(workspace)
					if json.Unmarshal(page, &envelope) != nil || envelope.SchemaVersion != 1 ||
						envelope.ProductID != a.writer.product || envelope.Environment != a.writer.environment ||
						envelope.Kind != kind || string(envelope.WorkspaceID) != string(tenant) || envelope.Items == nil || len(envelope.Items) > limit {
						return contract.ErrUnavailable
					}
					items = append(items, envelope.Items...)
					if string(envelope.NextID) == "null" {
						envelope.Items = items
						inventory, err = json.Marshal(envelope)
						return err
					}
					var next string
					if json.Unmarshal(envelope.NextID, &next) != nil || !maintenanceToken.MatchString(next) || (after != nil && next <= *after) || len(envelope.Items) != limit {
						return contract.ErrUnavailable
					}
					after = &next
				}
				return contract.ErrUnavailable
			})
			return inventory, err
		})
}
