package modelbinding

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct{ URL, StoreID, ModelID, Profile string }
type PinnedModel struct {
	config     Config
	compiled   contract.TenantModel
	credential func(context.Context) (string, error)
	client     *http.Client
}
type Verified struct {
	storeID, modelID string
	compiled         contract.TenantModel
}

func (v *Verified) StoreID() string   { return v.storeID }
func (v *Verified) ModelID() string   { return v.modelID }
func (v *Verified) ModelHash() string { return v.compiled.ModelHash }
func (v *Verified) Role(name string) (contract.RoleRelations, error) {
	return contract.ResolveRoleRelations(v.compiled, name)
}
func (v *Verified) Permission(name string) (string, error) {
	value, ok := v.compiled.Permissions[name]
	if !ok {
		return "", contract.ErrInvalidArgument
	}
	return value, nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var credentialValue = regexp.MustCompile(`^[\x21-\x7e]+$`)

// New requires manifest and binding to have passed structural schema validation.
func New(config Config, manifest contract.Manifest, binding contract.Binding, artifact contract.TenantModel, credential func(context.Context) (string, error)) (*PinnedModel, error) {
	if contract.ValidateEndpoint(config.URL, config.Profile) != nil || !identifier.MatchString(config.StoreID) || !identifier.MatchString(config.ModelID) || credential == nil {
		return nil, contract.ErrInvalidArgument
	}
	compiled, e := contract.CompileTenantModel(manifest, binding)
	if e != nil || !reflect.DeepEqual(compiled, artifact) {
		return nil, contract.ErrInvalidArgument
	}
	raw, e := json.Marshal(compiled)
	if e != nil || len(raw) > 1048576 {
		return nil, contract.ErrInvalidArgument
	}
	return &PinnedModel{config, compiled, credential, &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func cloneObject(value any) (map[string]any, bool) {
	original, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	result := map[string]any{}
	for k, v := range original {
		result[k] = v
	}
	return result, true
}
func userset(value any) (any, bool) {
	node, ok := cloneObject(value)
	if !ok {
		return nil, false
	}
	if child, exists := node["computedUserset"]; exists {
		computed, ok := cloneObject(child)
		if !ok {
			return nil, false
		}
		if _, exists := computed["object"]; !exists {
			computed["object"] = ""
		}
		node["computedUserset"] = computed
	}
	for _, name := range []string{"union", "intersection"} {
		if value, exists := node[name]; exists {
			operation, ok := cloneObject(value)
			if !ok {
				return nil, false
			}
			children, ok := operation["child"].([]any)
			if !ok {
				return nil, false
			}
			normalized := []any{}
			for _, child := range children {
				v, ok := userset(child)
				if !ok {
					return nil, false
				}
				normalized = append(normalized, v)
			}
			operation["child"] = normalized
			node[name] = operation
		}
	}
	if value, exists := node["difference"]; exists {
		difference, ok := cloneObject(value)
		if !ok {
			return nil, false
		}
		for _, name := range []string{"base", "subtract"} {
			v, ok := userset(difference[name])
			if !ok {
				return nil, false
			}
			difference[name] = v
		}
		node["difference"] = difference
	}
	return node, true
}
func wireModel(value any) (any, bool) {
	model, ok := cloneObject(value)
	if !ok {
		return nil, false
	}
	if _, exists := model["conditions"]; !exists {
		model["conditions"] = map[string]any{}
	}
	types, ok := model["type_definitions"].([]any)
	if !ok {
		return nil, false
	}
	normalized := []any{}
	for _, value := range types {
		definition, ok := cloneObject(value)
		if !ok {
			return nil, false
		}
		relations := map[string]any{}
		if value, exists := definition["relations"]; exists {
			var ok bool
			relations, ok = cloneObject(value)
			if !ok {
				return nil, false
			}
		}
		for name, value := range relations {
			v, ok := userset(value)
			if !ok {
				return nil, false
			}
			relations[name] = v
		}
		definition["relations"] = relations
		metadata := definition["metadata"]
		if metadata != nil {
			m, ok := cloneObject(metadata)
			if !ok {
				return nil, false
			}
			defaults(m)
			relations, ok := cloneObject(m["relations"])
			if !ok {
				return nil, false
			}
			for name, value := range relations {
				relation, ok := cloneObject(value)
				if !ok {
					return nil, false
				}
				defaults(relation)
				types, ok := relation["directly_related_user_types"].([]any)
				if !ok {
					return nil, false
				}
				normalized := []any{}
				for _, value := range types {
					r, ok := cloneObject(value)
					if !ok {
						return nil, false
					}
					if _, exists := r["condition"]; !exists {
						r["condition"] = ""
					}
					normalized = append(normalized, r)
				}
				relation["directly_related_user_types"] = normalized
				relations[name] = relation
			}
			m["relations"] = relations
			metadata = m
		}
		definition["metadata"] = metadata
		normalized = append(normalized, definition)
	}
	model["type_definitions"] = normalized
	return model, true
}
func defaults(m map[string]any) {
	if _, exists := m["module"]; !exists {
		m["module"] = ""
	}
	if _, exists := m["source_info"]; !exists {
		m["source_info"] = nil
	}
}
func (p *PinnedModel) Verify(ctx context.Context) (*Verified, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	raw, e := p.request(ctx, "GET", "/authorization-models/"+p.config.ModelID, nil, 1048576)
	if e != nil {
		return nil, e
	}
	var envelope map[string]any
	if json.Unmarshal(raw, &envelope) != nil || len(envelope) != 1 {
		return nil, contract.ErrUnavailable
	}
	model, ok := cloneObject(envelope["authorization_model"])
	if !ok || model["id"] != p.config.ModelID {
		return nil, contract.ErrUnavailable
	}
	delete(model, "id")
	actual, ok := wireModel(model)
	expected, valid := wireModel(p.compiled.Model)
	if !ok || !valid || !reflect.DeepEqual(actual, expected) {
		return nil, contract.ErrUnavailable
	}
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	return &Verified{p.config.StoreID, p.config.ModelID, p.compiled}, nil
}
func (p *PinnedModel) Check(ctx context.Context, user, workspace, permission string) (bool, error) {
	relation, ok := p.compiled.Permissions[permission]
	if !ok {
		return false, contract.ErrInvalidArgument
	}
	body, err := json.Marshal(map[string]any{
		"authorization_model_id": p.config.ModelID,
		"tuple_key":              map[string]string{"user": user, "object": "workspace:" + workspace, "relation": relation},
		"consistency":            "HIGHER_CONSISTENCY",
	})
	if err != nil {
		return false, contract.ErrUnavailable
	}
	raw, err := p.request(ctx, "POST", "/check", body, 65536)
	if err != nil {
		return false, err
	}
	var reply map[string]any
	if json.Unmarshal(raw, &reply) != nil {
		return false, contract.ErrUnavailable
	}
	allowed, ok := reply["allowed"].(bool)
	if !ok {
		return false, contract.ErrUnavailable
	}
	return allowed, contract.CheckCancellation(ctx)
}

func (p *PinnedModel) request(ctx context.Context, method, path string, body []byte, limit int64) ([]byte, error) {
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	type loaded struct {
		key string
		err error
	}
	done := make(chan loaded, 1)
	go func() { key, e := p.credential(ctx); done <- loaded{key, e} }()
	var key string
	select {
	case <-ctx.Done():
		return nil, contract.CheckCancellation(ctx)
	case r := <-done:
		if r.err != nil {
			return nil, failure(ctx)
		}
		key = r.key
	}
	if len(key) > 8192 || !credentialValue.MatchString(key) {
		return nil, contract.ErrUnavailable
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(p.config.URL, "/")+"/stores/"+p.config.StoreID+path, bytes.NewReader(body))
	if e != nil {
		return nil, contract.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, e := p.client.Do(req)
	if e != nil {
		return nil, failure(ctx)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, contract.ErrUnavailable
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if e != nil {
		return nil, failure(ctx)
	}
	if int64(len(raw)) > limit || !utf8.Valid(raw) {
		return nil, contract.ErrUnavailable
	}
	return raw, contract.CheckCancellation(ctx)
}

func failure(ctx context.Context) error {
	if e := contract.CheckCancellation(ctx); e != nil {
		return e
	}
	return contract.ErrUnavailable
}
