package heimdall

import (
	"context"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

// WorkspaceLifecyclePolicy authorizes a local suspension or resumption for the verified principal.
type WorkspaceLifecyclePolicy func(ctx context.Context, actor *Principal, workspaceID, action string) (bool, error)

// Suspend closes the local authorization gate and preserves any outstanding operation.
func (t *Tenant) Suspend(ctx context.Context, actor *Principal) error {
	return t.setLifecycle(ctx, actor, "suspend", "suspended")
}

// Resume opens the local gate only once outstanding operations are resolved.
func (t *Tenant) Resume(ctx context.Context, actor *Principal) error {
	return t.setLifecycle(ctx, actor, "resume", "active")
}

func (t *Tenant) setLifecycle(ctx context.Context, actor *Principal, action, lifecycle string) error {
	if ctx == nil || t == nil || t.client == nil || t.client.membership == nil || !tenantID.MatchString(t.id) {
		return ErrInvalidArgument
	}
	c := t.client
	w := c.onboarding
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	identity, err := w.identity(ctx, actor)
	if err != nil {
		return err
	}
	var before *postgres.ManagedWorkspace
	allowed, err := w.auth.CheckPermission(ctx, actor, t.id, "workspace."+action, w.cfg.OpenFGA.ModelID,
		w.cfg.ManifestVersion, func(operation context.Context) (bool, error) {
			if c.workspaceLifecycle == nil {
				return false, nil
			}
			var err error
			before, err = c.membership.workspace(operation, t.id)
			if err != nil {
				return false, err
			}
			return c.workspaceLifecycle(operation, actor, t.id, action)
		})
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	if before == nil {
		return ErrNotFound
	}
	if (before.Lifecycle != "active" && before.Lifecycle != "suspended") ||
		(action == "resume" && before.MutationBlocked) {
		return ErrConflict
	}
	return w.records.SetWorkspaceLifecycle(ctx, before, lifecycle, identity, postgres.OperationAllocation{
		EventID: onboardingID(), RequestID: onboardingID(), PolicyVersion: w.policyVersion,
		RecordedAt: max(time.Now().UnixMilli(), before.UpdatedAt),
	})
}
