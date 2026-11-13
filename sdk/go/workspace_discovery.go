package heimdall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/modelbinding"
)

type Workspace struct{ ID, Name string }
type Candidate struct {
	Workspace Workspace
	Position  string
}
type ScanOptions struct {
	After string
	Limit int
}
type CandidatePage struct {
	Items     []Candidate
	Exhausted bool
}
type WorkspaceSource interface {
	Scan(context.Context, ScanOptions) (CandidatePage, error)
}
type DiscoveryOptions struct {
	Source      WorkspaceSource
	ActiveKeyID string
	Keys        map[string][]byte
	ScanBudget  int
}
type WorkspaceListOptions struct {
	Cursor string
	Limit  int
}
type WorkspacePage struct {
	Items      []Workspace
	NextCursor string
}

type WorkspaceDiscovery struct {
	source  WorkspaceSource
	signer  *contract.CursorSigner
	budget  int
	runtime discoveryRuntime
}

type discoveryRuntime struct {
	scope   contract.CursorScope
	version int
	auth    *auditflow.Authentication
	model   *modelbinding.PinnedModel
}

func (c *Client) NewWorkspaceDiscovery(options DiscoveryOptions) (*WorkspaceDiscovery, error) {
	if c == nil || c.discovery == nil || options.Source == nil || options.ScanBudget < 0 || options.ScanBudget > 500 {
		return nil, ErrInvalidArgument
	}
	signer, err := contract.NewCursorSigner(options.ActiveKeyID, options.Keys)
	if err != nil {
		return nil, err
	}
	budget := options.ScanBudget
	if budget == 0 {
		budget = 500
	}
	return &WorkspaceDiscovery{options.Source, signer, budget, *c.discovery}, nil
}

type discoveryCursor struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	ProductID     string `json:"productId"`
	Environment   string `json:"environment"`
	Principal     string `json:"principal"`
	ModelID       string `json:"modelId"`
	Position      string `json:"position"`
	IssuedAt      int64  `json:"issuedAt"`
	ExpiresAt     int64  `json:"expiresAt"`
}

func discoveryText(value string, max int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= max
}

func (d *WorkspaceDiscovery) List(ctx context.Context, actor *Principal, options WorkspaceListOptions) (WorkspacePage, error) {
	empty := WorkspacePage{}
	if d == nil || d.source == nil || ctx == nil || options.Limit < 0 || options.Limit > 100 {
		return empty, ErrInvalidArgument
	}
	if !d.runtime.auth.Owns(actor) {
		return empty, ErrUnauthenticated
	}
	limit := options.Limit
	if limit == 0 {
		limit = 20
	}
	id := actor.Identity()
	user, err := contract.EncodeIdentity(id.Issuer, id.Subject)
	if err != nil {
		return empty, err
	}
	digest := sha256.Sum256([]byte(user))
	scope := d.runtime.scope
	scope.Principal = hex.EncodeToString(digest[:])
	after := ""
	if options.Cursor != "" {
		raw, err := d.signer.Verify(options.Cursor)
		var cursor discoveryCursor
		if err != nil || !contract.ValidJSONUnicode(raw) || json.Unmarshal(raw, &cursor) != nil ||
			cursor.SchemaVersion != 1 || cursor.Kind != "workspace_discovery" || !discoveryText(cursor.Position, 2048) ||
			cursor.IssuedAt < 0 || cursor.ExpiresAt > 9007199254740991 {
			return empty, ErrInvalidArgument
		}
		payload := contract.CursorPayload{
			CursorScope: contract.CursorScope{ProductID: cursor.ProductID, Environment: cursor.Environment, Principal: cursor.Principal, ModelID: cursor.ModelID},
			Position:    cursor.Position, IssuedAt: cursor.IssuedAt, ExpiresAt: cursor.ExpiresAt,
		}
		if err := contract.ValidateCursorScope(payload, scope, time.Now().UnixMilli()); err != nil {
			return empty, err
		}
		after = cursor.Position
	}
	result := WorkspacePage{Items: make([]Workspace, 0, limit)}
	exhausted := false
	_, err = d.runtime.auth.CheckLinkedPermission(ctx, actor, "", "workspaces.list", scope.ModelID, d.runtime.version, "", func(ctx context.Context, parent string) (bool, error) {
		positions := map[string]bool{after: true}
		ids := map[string]bool{}
		for consumed := 0; consumed < d.budget && len(result.Items) < limit && !exhausted; {
			size := min(20, d.budget-consumed)
			page, err := d.source.Scan(ctx, ScanOptions{after, size})
			if err != nil {
				return false, err
			}
			if err := contract.CheckCancellation(ctx); err != nil {
				return false, err
			}
			if len(page.Items) > size || (len(page.Items) == 0 && !page.Exhausted) {
				return false, ErrUnavailable
			}
			exhausted = page.Exhausted
			for index, candidate := range page.Items {
				item, position := candidate.Workspace, candidate.Position
				if !tenantID.MatchString(item.ID) || !discoveryText(item.Name, 256) || !discoveryText(position, 2048) ||
					ids[item.ID] || positions[position] {
					return false, ErrUnavailable
				}
				ids[item.ID], positions[position] = true, true
				allowed, err := d.runtime.auth.CheckLinkedPermission(ctx, actor, item.ID, "workspace.member", scope.ModelID, d.runtime.version, parent,
					func(ctx context.Context, _ string) (bool, error) {
						return d.runtime.model.CheckMembership(ctx, user, item.ID)
					})
				if err != nil {
					return false, err
				}
				after = position
				consumed++
				if allowed {
					result.Items = append(result.Items, item)
				}
				if len(result.Items) == limit {
					exhausted = page.Exhausted && index == len(page.Items)-1
					break
				}
			}
		}
		return len(result.Items) > 0, nil
	})
	if err != nil {
		return empty, err
	}
	if !exhausted && after != "" {
		now := time.Now().UnixMilli()
		raw, err := json.Marshal(discoveryCursor{1, "workspace_discovery", scope.ProductID, scope.Environment, scope.Principal, scope.ModelID, after, now, now + 900000})
		if err != nil {
			return empty, ErrUnavailable
		}
		result.NextCursor, err = d.signer.Sign(raw)
		if err != nil {
			return empty, err
		}
	}
	return result, nil
}
