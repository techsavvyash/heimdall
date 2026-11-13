package modelbinding

import (
	"context"
	"maps"
	"slices"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

// PlanMembership reads current direct grants and checks manifest delegation.
// The caller must serialize the snapshot and commit dispatch before invoking the plan.
func (p *PinnedModel) PlanMembership(ctx context.Context, ownerRole string, i contract.Intent) (func(context.Context, string) error, error) {
	return p.planMembership(ctx, ownerRole, i, false)
}

// PlanMembershipRecovery reconciles an already authorized intent after a verified recovery claim.
func (p *PinnedModel) PlanMembershipRecovery(ctx context.Context, ownerRole string, i contract.Intent) (func(context.Context, string) error, error) {
	return p.planMembership(ctx, ownerRole, i, true)
}

func (p *PinnedModel) planMembership(ctx context.Context, ownerRole string, i contract.Intent, recovering bool) (func(context.Context, string) error, error) {
	switch i.Kind {
	case "add_member", "assign_roles", "remove_member":
	default:
		return nil, contract.ErrInvalidArgument
	}
	if err := contract.ValidateIntent(i); err != nil {
		return nil, err
	}
	verified, err := p.Verify(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := verified.Role(ownerRole)
	if err != nil {
		return nil, err
	}
	desired := map[string]bool{}
	for _, role := range i.Roles {
		if role == ownerRole {
			return nil, contract.ErrInvalidArgument
		}
		if _, err := verified.Role(role); err != nil {
			return nil, err
		}
		desired[role] = true
	}
	user, _ := contract.EncodeIdentity(i.Target.Issuer, i.Target.Subject)
	admin, _ := contract.EncodeIdentity(i.Actor.Issuer, i.Actor.Subject)
	names := slices.Sorted(maps.Keys(verified.compiled.Roles))
	delegatedRoles := map[string]bool{}
	canDelegate := func(role string) (bool, error) {
		if allowed, ok := delegatedRoles[role]; ok {
			return allowed, nil
		}
		allowed, err := p.CheckDelegation(ctx, admin, i.WorkspaceID, role)
		delegatedRoles[role] = allowed
		return allowed, err
	}
	if !recovering {
		administrator, err := p.CheckRole(ctx, admin, i.WorkspaceID, ownerRole)
		if err != nil {
			return nil, err
		}
		for _, role := range names {
			if administrator {
				break
			}
			administrator, err = canDelegate(role)
			if err != nil {
				return nil, err
			}
		}
		if !administrator {
			return nil, contract.ErrForbidden
		}
	}
	current, err := p.directMembership(ctx, user, i.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if current[owner.Assignment] {
		return nil, contract.ErrForbidden
	}
	if i.Kind == "assign_roles" && !current["member"] {
		return nil, contract.ErrNotFound
	}
	if !recovering && i.Kind == "add_member" && current["member"] {
		return nil, contract.ErrConflict
	}
	next := map[string]bool{}
	if i.Kind != "remove_member" {
		next["member"] = true
	}
	for _, name := range names {
		relation := verified.compiled.Roles[name].Assignment
		if desired[name] {
			next[relation] = true
		}
		if recovering || (current[relation] == desired[name] && !(i.Kind == "add_member" && desired[name])) {
			continue
		}
		allowed, err := canDelegate(name)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, contract.ErrForbidden
		}
	}
	var writes, deletes []string
	for _, relation := range slices.Sorted(maps.Keys(next)) {
		if !current[relation] {
			writes = append(writes, relation)
		}
	}
	for _, relation := range slices.Sorted(maps.Keys(current)) {
		if !next[relation] {
			deletes = append(deletes, relation)
		}
	}
	return func(operation context.Context, operationID string) error {
		if err := contract.CheckCancellation(operation); err != nil {
			return err
		}
		unknown := contract.NewError(contract.OutcomeUnknown, operationID)
		if len(writes)+len(deletes) <= 100 {
			if err := p.writeDirectMembership(operation, user, i.WorkspaceID, writes, deletes); err != nil {
				return unknown
			}
		} else {
			// The managed gate stays closed for every batch of the same dispatch.
			for batch := range slices.Chunk(deletes, 100) {
				if err := p.writeDirectMembership(operation, user, i.WorkspaceID, nil, batch); err != nil {
					return unknown
				}
			}
			for batch := range slices.Chunk(writes, 100) {
				if err := p.writeDirectMembership(operation, user, i.WorkspaceID, batch, nil); err != nil {
					return unknown
				}
			}
		}
		observed, err := p.directMembership(operation, user, i.WorkspaceID)
		if err != nil || !maps.Equal(observed, next) {
			return unknown
		}
		return nil
	}, nil
}
