package modelbinding

import (
	"context"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// PlanOwnershipTransfer runs under the managed operation journal. Recovery callers
// must hold a generation claimed after verifying writer quiescence.
func (p *PinnedModel) PlanOwnershipTransfer(ctx context.Context, ownerRole string, intent contract.Intent, recovering bool) (func(context.Context, string) error, error) {
	source, err := contract.EncodeIdentity(intent.Actor.Issuer, intent.Actor.Subject)
	if err != nil {
		return nil, err
	}
	target, err := contract.EncodeIdentity(intent.Target.Issuer, intent.Target.Subject)
	if err != nil || target == source {
		return nil, contract.ErrInvalidArgument
	}
	verified, err := p.Verify(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := verified.Role(ownerRole)
	if err != nil {
		return nil, err
	}
	workspace := intent.WorkspaceID
	if !recovering {
		allowed, err := p.CheckRole(ctx, source, workspace, ownerRole)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, contract.ErrForbidden
		}
	}
	current, err := p.directMembership(ctx, target, workspace)
	if err != nil {
		return nil, err
	}
	if !current["member"] {
		return nil, contract.ErrConflict
	}
	previous, err := p.directMembership(ctx, source, workspace)
	if err != nil {
		return nil, err
	}
	if !current[owner.Assignment] && (!previous[owner.Assignment] || !previous["member"]) {
		return nil, contract.ErrConflict
	}
	return func(operation context.Context, id string) error {
		unknown := contract.NewError(contract.OutcomeUnknown, id)
		if !current[owner.Assignment] {
			if err := p.writeDirectMembership(operation, target, workspace, []string{owner.Assignment}, nil); err != nil {
				return unknown
			}
		}
		confirmed, err := p.directMembership(operation, target, workspace)
		if err != nil || !confirmed["member"] || !confirmed[owner.Assignment] {
			return unknown
		}
		if previous[owner.Assignment] {
			if err := p.writeDirectMembership(operation, source, workspace, nil, []string{owner.Assignment}); err != nil {
				return unknown
			}
		}
		former, err := p.directMembership(operation, source, workspace)
		if err != nil || former[owner.Assignment] {
			return unknown
		}
		return nil
	}, nil
}
