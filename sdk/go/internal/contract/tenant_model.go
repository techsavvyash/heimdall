package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

type RoleRelations struct {
	Assignment string `json:"assignment"`
	Effective  string `json:"effective"`
	Delegate   string `json:"delegate"`
}
type TenantModel struct {
	Model       map[string]any           `json:"model"`
	ModelHash   string                   `json:"modelHash"`
	Roles       map[string]RoleRelations `json:"roles"`
	Permissions map[string]string        `json:"permissions"`
}

func modelRef(relation string) map[string]any {
	return map[string]any{"computedUserset": map[string]any{"relation": relation}}
}
func modelIntersect(left, right any) map[string]any {
	return map[string]any{"intersection": map[string]any{"child": []any{left, right}}}
}

// CompileTenantModel requires structural schema validation before cross-reference validation.
// Ordinals are meaningful only with the pinned model and manifest that generated them.
func CompileTenantModel(manifest Manifest, binding Binding) (TenantModel, error) {
	if err := ValidateManifestBinding(manifest, binding); err != nil {
		return TenantModel{}, err
	}
	result := TenantModel{Roles: map[string]RoleRelations{}, Permissions: map[string]string{}}
	relations := map[string]any{"member": map[string]any{"this": map[string]any{}}}
	direct := map[string]any{"directly_related_user_types": []any{map[string]any{"type": "user"}}}
	metadata := map[string]any{"member": direct}
	names := make([]string, 0, len(manifest.Roles))
	for role := range manifest.Roles {
		names = append(names, role)
	}
	sort.Strings(names)
	for index, role := range names {
		r := RoleRelations{Assignment: fmt.Sprintf("r_%d", index), Effective: fmt.Sprintf("e_%d", index), Delegate: fmt.Sprintf("d_%d", index)}
		result.Roles[role] = r
		relations[r.Assignment] = map[string]any{"this": map[string]any{}}
		metadata[r.Assignment] = direct
		relations[r.Effective] = modelIntersect(modelRef("member"), modelRef(r.Assignment))
	}
	permissions := slices.Clone(manifest.Permissions)
	sort.Strings(permissions)
	for index, permission := range permissions {
		relation := fmt.Sprintf("p_%d", index)
		result.Permissions[permission] = relation
		grants := []any{}
		for _, role := range names {
			if slices.Contains(manifest.Roles[role], permission) {
				grants = append(grants, modelRef(result.Roles[role].Assignment))
			}
		}
		relations[relation] = modelIntersect(modelRef("member"), modelUnion(grants))
	}
	for _, target := range names {
		grants := []any{}
		if target != manifest.OwnerRole {
			for _, actor := range names {
				if slices.Contains(manifest.Delegation[actor], target) {
					grants = append(grants, modelRef(result.Roles[actor].Assignment))
				}
			}
		}
		relations[result.Roles[target].Delegate] = modelIntersect(modelRef("member"), modelUnion(grants))
	}

	result.Model = map[string]any{"schema_version": "1.1", "type_definitions": []any{map[string]any{"type": "user"}, map[string]any{"type": "workspace", "relations": relations, "metadata": map[string]any{"relations": metadata}}}}
	encoded, err := json.Marshal(result.Model)
	if err != nil {
		return TenantModel{}, err
	}
	sum := sha256.Sum256(encoded)
	result.ModelHash = hex.EncodeToString(sum[:])
	return result, nil
}

func modelUnion(grants []any) any {
	switch len(grants) {
	case 0:
		return map[string]any{"difference": map[string]any{"base": modelRef("member"), "subtract": modelRef("member")}}
	case 1:
		return grants[0]
	default:
		return map[string]any{"union": map[string]any{"child": grants}}
	}
}

// ResolveRoleRelations resolves names only. Mutation callers must check Delegate
// against fresh actor membership and persist audit before dispatch.
func ResolveRoleRelations(model TenantModel, role string) (RoleRelations, error) {
	relations, ok := model.Roles[role]
	if !ok {
		return RoleRelations{}, ErrInvalidArgument
	}
	return relations, nil
}
