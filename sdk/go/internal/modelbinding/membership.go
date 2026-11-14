package modelbinding

import (
	"context"
	"encoding/json"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type membershipTuple struct {
	User     string `json:"user"`
	Object   string `json:"object"`
	Relation string `json:"relation,omitempty"`
}

func (p *PinnedModel) CheckDelegation(ctx context.Context, user, workspace, role string) (bool, error) {
	relations, err := contract.ResolveRoleRelations(p.compiled, role)
	if err != nil {
		return false, err
	}
	return p.checkRelation(ctx, user, workspace, relations.Delegate)
}

func (p *PinnedModel) directMembership(ctx context.Context, user, workspace string) (map[string]bool, error) {
	object := "workspace:" + workspace
	known := map[string]bool{"member": true}
	for _, role := range p.compiled.Roles {
		known[role.Assignment] = true
	}
	result := map[string]bool{}
	cursor := ""
	for range 2 {
		body := map[string]any{"tuple_key": membershipTuple{User: user, Object: object},
			"consistency": "HIGHER_CONSISTENCY", "page_size": 100}
		if cursor != "" {
			body["continuation_token"] = cursor
		}
		request, _ := json.Marshal(body)
		raw, err := p.request(ctx, "POST", "/read", request, 1<<20)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tuples *[]struct {
				Key struct {
					membershipTuple
					Condition any `json:"condition"`
				}
			}
			Continuation json.RawMessage `json:"continuation_token"`
		}
		if json.Unmarshal(raw, &page) != nil || page.Tuples == nil || len(*page.Tuples) > 100 {
			return nil, contract.ErrUnavailable
		}
		for _, row := range *page.Tuples {
			key := row.Key
			if key.Object != object || key.User != user || !known[key.Relation] || result[key.Relation] || key.Condition != nil {
				return nil, contract.ErrUnavailable
			}
			result[key.Relation] = true
		}
		var next string
		if len(page.Continuation) > 0 && (page.Continuation[0] != '"' || json.Unmarshal(page.Continuation, &next) != nil) {
			return nil, contract.ErrUnavailable
		}
		if next == "" {
			return result, nil
		}
		if len(*page.Tuples) == 0 || next == cursor {
			return nil, contract.ErrUnavailable
		}
		cursor = next
	}
	return nil, contract.ErrUnavailable
}

func (p *PinnedModel) writeDirectMembership(ctx context.Context, user, workspace string, writes, deletes []string) error {
	if len(writes)+len(deletes) == 0 {
		return nil
	}
	request := map[string]any{"authorization_model_id": p.config.ModelID}
	for kind, relations := range map[string][]string{"writes": writes, "deletes": deletes} {
		if len(relations) == 0 {
			continue
		}
		tuples := make([]membershipTuple, 0, len(relations))
		for _, relation := range relations {
			tuples = append(tuples, membershipTuple{user, "workspace:" + workspace, relation})
		}
		request[kind] = map[string]any{"tuple_keys": tuples}
	}
	body, _ := json.Marshal(request)
	raw, err := p.request(ctx, "POST", "/write", body, 65536)
	if err != nil {
		return err
	}
	var reply map[string]any
	if json.Unmarshal(raw, &reply) != nil || reply == nil {
		return contract.ErrUnavailable
	}
	return nil
}

func (p *PinnedModel) ReadDirectMembers(ctx context.Context, workspace, cursor string) ([]string, string, error) {
	object := "workspace:" + workspace
	body := map[string]any{"tuple_key": map[string]string{"object": object, "relation": "member"},
		"consistency": "HIGHER_CONSISTENCY", "page_size": 20}
	if cursor != "" {
		body["continuation_token"] = cursor
	}
	request, _ := json.Marshal(body)
	raw, err := p.request(ctx, "POST", "/read", request, 1048576)
	if err != nil {
		return nil, "", err
	}
	var page struct {
		Tuples *[]struct {
			Key struct {
				membershipTuple
				Condition any `json:"condition"`
			}
		}
		Continuation string `json:"continuation_token"`
	}
	if json.Unmarshal(raw, &page) != nil || page.Tuples == nil || len(*page.Tuples) > 20 ||
		len(page.Continuation) > 8192 || (page.Continuation != "" && (page.Continuation == cursor || len(*page.Tuples) == 0)) {
		return nil, "", contract.ErrUnavailable
	}
	users := make([]string, 0, len(*page.Tuples))
	seen := make(map[string]bool)
	for _, row := range *page.Tuples {
		key := row.Key
		if key.Object != object || key.Relation != "member" || key.User == "" || key.Condition != nil || seen[key.User] {
			return nil, "", contract.ErrUnavailable
		}
		users = append(users, key.User)
		seen[key.User] = true
	}
	return users, page.Continuation, nil
}
