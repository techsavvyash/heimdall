package heimdall

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/modelbinding"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type MembershipInput struct {
	Target         Identity
	IdempotencyKey string
}

type MembershipRolesInput struct {
	MembershipInput
	Roles []string
}

type MembershipStatus struct {
	OperationID string `json:"operationId"`
	WorkspaceID string `json:"workspaceId"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
}

type Memberships struct {
	tenant *Tenant
}

type membershipRuntime struct {
	ownerRole string
	model     *modelbinding.PinnedModel
	workspace func(context.Context, string) (*postgres.ManagedWorkspace, error)
}

func (t *Tenant) Memberships() *Memberships {
	if t == nil || t.client == nil || t.client.membership == nil {
		return nil
	}
	return &Memberships{tenant: t}
}

func (m *Memberships) Add(ctx context.Context, actor *Principal, input MembershipRolesInput) (MembershipStatus, error) {
	return m.mutate(ctx, actor, "add_member", input)
}
func (m *Memberships) AssignRoles(ctx context.Context, actor *Principal, input MembershipRolesInput) (MembershipStatus, error) {
	return m.mutate(ctx, actor, "assign_roles", input)
}
func (m *Memberships) Remove(ctx context.Context, actor *Principal, input MembershipInput) (MembershipStatus, error) {
	return m.mutate(ctx, actor, "remove_member", MembershipRolesInput{MembershipInput: input, Roles: []string{}})
}

// TransferOwnership promotes an eligible existing member before removing the actor's owner grant.
func (m *Memberships) TransferOwnership(ctx context.Context, actor *Principal, input MembershipInput) (MembershipStatus, error) {
	return m.mutate(ctx, actor, "transfer_owner", MembershipRolesInput{MembershipInput: input, Roles: []string{}})
}

func memberStatus(op *postgres.ManagedOperation) MembershipStatus {
	result := MembershipStatus{OperationID: op.OperationID, WorkspaceID: op.WorkspaceID, Status: "pending"}
	switch op.State {
	case "completed":
		result.Status = "confirmed"
	case "uncertain", "dispatching":
		result.Reason = "recovery_required"
	}
	return result
}

func (m *Memberships) mutate(ctx context.Context, principal *Principal, kind string, input MembershipRolesInput) (MembershipStatus, error) {
	empty := MembershipStatus{}
	if ctx == nil || m == nil || m.tenant == nil || !tenantID.MatchString(m.tenant.id) {
		return empty, ErrInvalidArgument
	}
	c, workspaceID := m.tenant.client, m.tenant.id
	w, port := c.onboarding, c.membership
	actor, err := w.identity(ctx, principal)
	if err != nil {
		return empty, err
	}
	if input.Target.Issuer != w.cfg.Issuer || input.Roles == nil {
		return empty, ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	intent := contract.Intent{
		SchemaVersion: 1, ProductID: w.cfg.ProductID, Environment: w.cfg.Environment, Kind: kind,
		Actor: actor, Target: contract.Identity{Issuer: input.Target.Issuer, Subject: input.Target.Subject},
		WorkspaceID: workspaceID, Roles: slices.Clone(input.Roles), ManifestVersion: w.cfg.ManifestVersion,
	}
	if _, err := contract.IntentDigests(intent, input.IdempotencyKey); err != nil {
		return empty, err
	}
	var existing *postgres.ManagedOperation
	var revision string
	var apply func(context.Context, string) error
	action := "membership." + kind
	if kind == "transfer_owner" {
		action = "ownership.transfer"
	}
	allowed, err := w.auth.CheckPermission(ctx, principal, workspaceID, action,
		w.cfg.OpenFGA.ModelID, w.cfg.ManifestVersion, func(operation context.Context) (bool, error) {
			var err error
			existing, err = w.records.FindIntent(operation, intent, input.IdempotencyKey)
			if err != nil {
				return false, err
			}
			if existing != nil && existing.State != "pending" {
				return true, nil
			}
			workspace, err := port.workspace(operation, workspaceID)
			if err != nil || workspace == nil || workspace.Lifecycle != "active" || workspace.MutationBlocked != (existing != nil) {
				return false, err
			}
			revision = workspace.Revision
			if kind == "transfer_owner" {
				apply, err = port.model.PlanOwnershipTransfer(operation, port.ownerRole, intent, false)
			} else {
				apply, err = port.model.PlanMembership(operation, port.ownerRole, intent)
			}
			return err == nil, err
		})
	if err != nil {
		return empty, err
	}
	if !allowed {
		return empty, ErrForbidden
	}
	op := existing
	requestID := onboardingID()
	if op == nil {
		raw, err := json.Marshal(intent)
		if err != nil {
			return empty, ErrInvalidArgument
		}
		id, at := onboardingID(), time.Now().UnixMilli()
		op, err = w.records.BeginMembership(ctx, raw, input.IdempotencyKey, postgres.OperationAllocation{
			OperationID: id, WorkspaceID: workspaceID, EventID: onboardingID(), RequestID: requestID,
			PolicyVersion: w.policyVersion, OccurredAt: at, RecordedAt: at,
		}, revision)
		if err != nil {
			return empty, err
		}
		if op.OperationID != id {
			return memberStatus(op), nil
		}
	}
	advance := func(transition string) error {
		at := max(time.Now().UnixMilli(), op.UpdatedAt)
		next, err := w.records.Advance(ctx, op.OperationID, actor, op.Generation, transition, postgres.OperationTransitionMetadata{
			EventID: onboardingID(), RequestID: requestID, PolicyVersion: w.policyVersion,
			OccurredAt: at, RecordedAt: at, NextAttemptAt: at,
		})
		if err == nil {
			op = next
		}
		return err
	}
	resolve := func(err error) (MembershipStatus, error) {
		if errors.Is(err, ErrConflict) && ctx.Err() == nil {
			current, readErr := w.records.Get(ctx, op.OperationID, actor)
			if readErr != nil {
				return empty, readErr
			}
			if current != nil && current.Generation != op.Generation {
				return memberStatus(current), nil
			}
		}
		return empty, err
	}
	if op.State == "pending" && apply != nil {
		if err := advance("dispatch"); err != nil {
			return resolve(err)
		}
		if err := apply(ctx, op.OperationID); err != nil {
			if ctx.Err() == nil {
				if err := advance("uncertain"); err != nil {
					return resolve(err)
				}
			}
			return memberStatus(op), nil
		}
		if err := advance("confirm"); err != nil {
			return resolve(err)
		}
	}
	if op.State == "confirmed" {
		if err := advance("complete"); err != nil {
			return resolve(err)
		}
	}
	return memberStatus(op), nil
}
