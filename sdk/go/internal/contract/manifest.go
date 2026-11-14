package contract

type Manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	ProductID     string `json:"productId"`
	Version       int    `json:"version"`
	Clients       map[string]struct {
		RedirectURIs     []string `json:"redirectUris"`
		TransportProfile string   `json:"transportProfile,omitempty"`
	} `json:"clients"`
	Permissions []string            `json:"permissions"`
	Roles       map[string][]string `json:"roles"`
	OwnerRole   string              `json:"ownerRole"`
	Delegation  map[string][]string `json:"delegation"`
}
type Binding struct {
	ProductID        string `json:"productId"`
	ManifestVersion  int    `json:"manifestVersion"`
	ClientKey        string `json:"clientKey"`
	TransportProfile string `json:"transportProfile,omitempty"`
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
