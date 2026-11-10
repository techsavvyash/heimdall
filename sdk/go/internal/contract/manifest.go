package contract

type Manifest struct {
	SchemaVersion int
	ProductID     string
	Version       int
	Clients       map[string]struct{ RedirectURIs []string }
	Permissions   []string
	Roles         map[string][]string
	OwnerRole     string
	Delegation    map[string][]string
}
type Binding struct {
	ProductID       string
	ManifestVersion int
	ClientKey       string
}

// ValidateManifestBinding checks cross references after structural validation.
func ValidateManifestBinding(manifest Manifest, config Binding) error {
	_, clientExists := manifest.Clients[config.ClientKey]
	_, ownerExists := manifest.Roles[manifest.OwnerRole]
	if config.ProductID != manifest.ProductID || config.ManifestVersion != manifest.Version ||
		!clientExists || !ownerExists {
		return ErrInvalidArgument
	}
	permissions := make(map[string]bool, len(manifest.Permissions))
	for _, p := range manifest.Permissions {
		permissions[p] = true
	}
	for _, grants := range manifest.Roles {
		for _, p := range grants {
			if !permissions[p] {
				return ErrInvalidArgument
			}
		}
	}
	for actor, targets := range manifest.Delegation {
		if _, ok := manifest.Roles[actor]; !ok {
			return ErrInvalidArgument
		}
		for _, target := range targets {
			if _, ok := manifest.Roles[target]; !ok || target == manifest.OwnerRole {
				return ErrInvalidArgument
			}
		}
	}
	return nil
}
