package provisioning

import (
	"regexp"
	"slices"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type DirectoryGrantSource struct {
	Kind, RuleID string
	Roles        []string
}

type DirectoryRevocationInput struct {
	RuleID, OwnerRole string
	CurrentRoles      []string
	Sources           []DirectoryGrantSource
}

type DirectoryRevocationPlan struct {
	Action string
	Roles  []string
}

var directoryRoleName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func directoryRoleList(roles []string) bool {
	if roles == nil || len(roles) > 128 {
		return false
	}
	seen := make(map[string]bool, len(roles))
	for _, role := range roles {
		if !directoryRoleName.MatchString(role) || seen[role] {
			return false
		}
		seen[role] = true
	}
	return true
}

// PlanDirectoryRevocation uses current direct roles and complete product-owned grant provenance.
func PlanDirectoryRevocation(in DirectoryRevocationInput) (DirectoryRevocationPlan, error) {
	empty := DirectoryRevocationPlan{}
	if !providerID.MatchString(in.RuleID) || !directoryRoleName.MatchString(in.OwnerRole) ||
		!directoryRoleList(in.CurrentRoles) || in.Sources == nil || len(in.Sources) > 128 {
		return empty, contract.ErrInvalidArgument
	}
	retained, rules := map[string]bool{}, map[string]bool{}
	var removed []string
	keepsMembership := false
	for _, source := range in.Sources {
		if !directoryRoleList(source.Roles) {
			return empty, contract.ErrInvalidArgument
		}
		switch source.Kind {
		case "directory":
			if !providerID.MatchString(source.RuleID) || rules[source.RuleID] || slices.Contains(source.Roles, in.OwnerRole) {
				return empty, contract.ErrInvalidArgument
			}
			rules[source.RuleID] = true
			if source.RuleID == in.RuleID {
				removed = source.Roles
				continue
			}
		case "independent":
		default:
			return empty, contract.ErrInvalidArgument
		}
		keepsMembership = true
		for _, role := range source.Roles {
			retained[role] = true
		}
	}
	if removed == nil {
		return empty, contract.ErrNotFound
	}
	if slices.Contains(in.CurrentRoles, in.OwnerRole) {
		return empty, contract.ErrForbidden
	}
	roles := make([]string, 0, len(in.CurrentRoles))
	for _, role := range in.CurrentRoles {
		if !slices.Contains(removed, role) || retained[role] {
			roles = append(roles, role)
		}
	}
	slices.Sort(roles)
	action := "remove_member"
	if keepsMembership || len(roles) > 0 {
		action = "assign_roles"
		if len(roles) == len(in.CurrentRoles) {
			action = "unchanged"
		}
	}
	return DirectoryRevocationPlan{action, roles}, nil
}
