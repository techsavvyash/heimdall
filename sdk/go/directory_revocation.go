package heimdall

import (
	"context"
	"slices"

	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

type DirectoryGrantRevocation struct {
	provisioning.DirectoryRevocationInput
	Target         Identity
	IdempotencyKey string
}

// RevokeDirectoryGrant applies an approved grant-source snapshot through managed membership.
func (m *Memberships) RevokeDirectoryGrant(ctx context.Context, actor *Principal, input DirectoryGrantRevocation) (MembershipStatus, error) {
	if ctx == nil || m == nil {
		return MembershipStatus{}, ErrInvalidArgument
	}
	plan, err := provisioning.PlanDirectoryRevocation(input.DirectoryRevocationInput)
	if err != nil {
		return MembershipStatus{}, err
	}
	change := MembershipInput{Target: input.Target, IdempotencyKey: input.IdempotencyKey, ExpectedDirectRoles: slices.Clone(input.CurrentRoles)}
	if plan.Action == "remove_member" {
		return m.Remove(ctx, actor, change)
	}
	return m.AssignRoles(ctx, actor, MembershipRolesInput{MembershipInput: change, Roles: plan.Roles})
}
