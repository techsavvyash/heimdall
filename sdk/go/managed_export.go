package heimdall

import (
	"context"

	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type ManagedExportRequest = auditflow.ManagedExportRequest
type ManagedExportScope = auditflow.MaintenanceContext
type ManagedExportPolicy func(context.Context, ManagedExportScope) (bool, error)

type managedExportRuntime struct {
	reader          *postgres.ManagedExport
	policy          ManagedExportPolicy
	modelID         string
	manifestVersion int
}

// ExportManagedPage releases one sanitized page after authorization and durable audit.
func (c *Client) ExportManagedPage(ctx context.Context, token string, request ManagedExportRequest) ([]byte, error) {
	if c == nil || c.managedExport == nil {
		return nil, ErrInvalidArgument
	}
	port := c.managedExport
	return c.authentication.RunManagedExport(ctx, token, request, port.modelID, port.manifestVersion, port.policy, port.reader.Scan)
}

// ExportManagedInventory releases a bounded inventory from one database snapshot.
func (c *Client) ExportManagedInventory(ctx context.Context, token string, request ManagedExportRequest) ([]byte, error) {
	if c == nil || c.managedExport == nil {
		return nil, ErrInvalidArgument
	}
	port := c.managedExport
	return c.authentication.RunManagedInventory(ctx, token, request, port.modelID, port.manifestVersion, port.policy, port.reader.WithSnapshot)
}
