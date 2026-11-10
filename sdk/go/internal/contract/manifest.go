package contract

type Manifest struct {
	SchemaVersion int
	ProductID     string
	Version       int
	Clients       map[string]struct {
		RedirectURIs     []string
		TransportProfile string
	}
	Permissions []string
	Roles       map[string][]string
	OwnerRole   string
	Delegation  map[string][]string
}
type Binding struct {
	ProductID        string
	ManifestVersion  int
	ClientKey        string
	TransportProfile string
}

// ValidateManifestBinding checks cross references after structural validation.
func ValidateManifestBinding(manifest Manifest, config Binding) error {
	_, clientExists := manifest.Clients[config.ClientKey]
	_, ownerExists := manifest.Roles[manifest.OwnerRole]
	if config.ProductID != manifest.ProductID || config.ManifestVersion != manifest.Version ||
		!clientExists || !ownerExists {
		return ErrInvalidArgument
	}
	profile := config.TransportProfile
	if profile == "" {
		profile = "production"
	}
	selected := manifest.Clients[config.ClientKey].TransportProfile
	if selected == "" {
		selected = "production"
	}
	if profile != selected {
		return ErrInvalidArgument
	}
	for _, client := range manifest.Clients {
		p := client.TransportProfile
		if p == "" {
			p = "production"
		}
		for _, uri := range client.RedirectURIs {
			if err := ValidateEndpoint(uri, p); err != nil {
				return err
			}
		}
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
