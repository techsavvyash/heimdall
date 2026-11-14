package configuration

import (
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"math"
	"regexp"
	"unicode/utf8"
)

var slug = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var role = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var permission = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*$`)
var token = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var redirect = regexp.MustCompile(`^https?://[^/]+/`)

func text(v any, max int, pattern *regexp.Regexp) bool {
	s, ok := v.(string)
	return ok && utf8.ValidString(s) && utf8.RuneCountInString(s) > 0 && utf8.RuneCountInString(s) <= max && (pattern == nil || pattern.MatchString(s))
}
func fields(v any, required, optional []string) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, k := range required {
		allowed[k] = true
		if _, exists := m[k]; !exists {
			return nil, false
		}
	}
	for _, k := range optional {
		allowed[k] = true
	}
	for k := range m {
		if !allowed[k] {
			return nil, false
		}
	}
	return m, true
}
func version(v any) bool {
	n, ok := v.(float64)
	return ok && n >= 1 && n <= 2147483647 && math.Trunc(n) == n
}
func profile(m map[string]any) (string, bool) {
	v, exists := m["transportProfile"]
	if !exists {
		return "production", true
	}
	s, ok := v.(string)
	return s, ok && (s == "production" || s == "loopback")
}
func list(v any, min, max int, check func(any) bool) bool {
	a, ok := v.([]any)
	if !ok || len(a) < min || len(a) > max {
		return false
	}
	seen := map[string]bool{}
	for _, v := range a {
		if !check(v) {
			return false
		}
		s := v.(string)
		if seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func entries(v any, min, max int, pattern *regexp.Regexp) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) < min || len(m) > max {
		return nil, false
	}
	for k := range m {
		if !text(k, 64, pattern) {
			return nil, false
		}
	}
	return m, true
}
func endpoint(v any, p string) bool {
	return text(v, 2048, nil) && contract.ValidateEndpoint(v.(string), p) == nil
}
func ValidateDeploymentShape(value any) error {
	c, ok := fields(value, []string{"schemaVersion", "productId", "environment", "issuer", "audience", "clientId", "zitadelProjectId", "clientKey", "manifestVersion", "openfga"}, []string{"transportProfile"})
	if !ok || c["schemaVersion"] != float64(1) {
		return contract.ErrInvalidArgument
	}
	for _, k := range []string{"productId", "environment", "clientKey"} {
		if !text(c[k], 64, slug) {
			return contract.ErrInvalidArgument
		}
	}
	clientID, ok := c["clientId"].(string)
	if !ok || !contract.ValidClientID(clientID) || !text(c["zitadelProjectId"], 128, token) {
		return contract.ErrInvalidArgument
	}
	p, ok := profile(c)
	if !ok || !text(c["audience"], 2048, nil) || !version(c["manifestVersion"]) || !endpoint(c["issuer"], p) {
		return contract.ErrInvalidArgument
	}
	f, ok := fields(c["openfga"], []string{"url", "storeId", "modelId"}, nil)
	if !ok || !endpoint(f["url"], p) || !text(f["storeId"], 128, token) || !text(f["modelId"], 128, token) {
		return contract.ErrInvalidArgument
	}
	return nil
}
func ValidateManifestShape(value any) error {
	m, ok := fields(value, []string{"schemaVersion", "productId", "version", "clients", "permissions", "roles", "ownerRole", "delegation"}, nil)
	if !ok || m["schemaVersion"] != float64(1) || !text(m["productId"], 64, slug) || !version(m["version"]) || !text(m["ownerRole"], 64, role) {
		return contract.ErrInvalidArgument
	}
	clients, ok := entries(m["clients"], 1, 32, slug)
	if !ok {
		return contract.ErrInvalidArgument
	}
	for _, v := range clients {
		c, ok := fields(v, []string{"redirectUris"}, []string{"transportProfile"})
		if !ok {
			return contract.ErrInvalidArgument
		}
		p, ok := profile(c)
		if !ok || !list(c["redirectUris"], 1, 32, func(v any) bool { return endpoint(v, p) && redirect.MatchString(v.(string)) }) {
			return contract.ErrInvalidArgument
		}
	}
	checkPermission := func(v any) bool { return text(v, 128, permission) }
	if !list(m["permissions"], 1, 256, checkPermission) {
		return contract.ErrInvalidArgument
	}
	roles, ok := entries(m["roles"], 1, 128, role)
	if !ok {
		return contract.ErrInvalidArgument
	}
	for _, v := range roles {
		if !list(v, 0, 256, checkPermission) {
			return contract.ErrInvalidArgument
		}
	}
	delegation, ok := entries(m["delegation"], 0, 128, role)
	if !ok {
		return contract.ErrInvalidArgument
	}
	for _, v := range delegation {
		if !list(v, 0, 128, func(v any) bool { return text(v, 64, role) }) {
			return contract.ErrInvalidArgument
		}
	}
	return nil
}

type Config struct {
	SchemaVersion    int    `json:"schemaVersion"`
	ProductID        string `json:"productId"`
	Environment      string `json:"environment"`
	Issuer           string `json:"issuer"`
	Audience         string `json:"audience"`
	ClientID         string `json:"clientId"`
	ZitadelProjectID string `json:"zitadelProjectId"`
	ClientKey        string `json:"clientKey"`
	ManifestVersion  int    `json:"manifestVersion"`
	OpenFGA          struct {
		URL     string `json:"url"`
		StoreID string `json:"storeId"`
		ModelID string `json:"modelId"`
	} `json:"openfga"`
	TransportProfile string `json:"transportProfile,omitempty"`
}
type Deployment struct {
	config   Config
	manifest contract.Manifest
	artifact contract.TenantModel
}

func (d *Deployment) Config() Config { return d.config }

// Values containing maps/slices are detached copies, never mutable access to the binding.
func (d *Deployment) Manifest() contract.Manifest {
	raw, _ := json.Marshal(d.manifest)
	var result contract.Manifest
	_ = json.Unmarshal(raw, &result)
	return result
}
func (d *Deployment) Artifact() contract.TenantModel {
	raw, _ := json.Marshal(d.artifact)
	var result contract.TenantModel
	_ = json.Unmarshal(raw, &result)
	return result
}
func Parse(configJSON, manifestJSON []byte) (*Deployment, error) {
	if len(configJSON) > 1048576 || len(manifestJSON) > 1048576 || !contract.ValidJSONUnicode(configJSON) || !contract.ValidJSONUnicode(manifestJSON) {
		return nil, contract.ErrInvalidArgument
	}
	var c, m any
	if json.Unmarshal(configJSON, &c) != nil || json.Unmarshal(manifestJSON, &m) != nil || ValidateDeploymentShape(c) != nil || ValidateManifestShape(m) != nil {
		return nil, contract.ErrInvalidArgument
	}
	// Canonicalize numeric JSON values before decoding integer Go fields (1e0 is integer 1).
	configJSON, _ = json.Marshal(c)
	manifestJSON, _ = json.Marshal(m)
	var config Config
	var manifest contract.Manifest
	if json.Unmarshal(configJSON, &config) != nil || json.Unmarshal(manifestJSON, &manifest) != nil {
		return nil, contract.ErrInvalidArgument
	}
	binding := contract.Binding{ProductID: config.ProductID, ManifestVersion: config.ManifestVersion, ClientKey: config.ClientKey, TransportProfile: config.TransportProfile}
	artifact, e := contract.CompileTenantModel(manifest, binding)
	if e != nil {
		return nil, e
	}
	return &Deployment{config, manifest, artifact}, nil
}
