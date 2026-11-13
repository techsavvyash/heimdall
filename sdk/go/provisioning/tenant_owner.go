package provisioning

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/modelbinding"
)

type TenantOwnerInput struct {
	OperationID, TenantID string
	Issuer, Subject       string
}

type TenantOwnerReceipt struct {
	OperationID string `json:"operationId"`
	TenantID    string `json:"tenantId"`
	Principal   string `json:"principal"`
	StoreID     string `json:"storeId"`
	ModelID     string `json:"modelId"`
}

type ownerTuple struct {
	User     string `json:"user,omitempty"`
	Object   string `json:"object"`
	Relation string `json:"relation"`
}

// EnsureTenantOwner assumes a serialized, authorized pending-intent workflow.
func EnsureTenantOwner(ctx context.Context, configJSON, manifestJSON []byte, token string, input TenantOwnerInput, beforeWrite func(context.Context) error) (TenantOwnerReceipt, error) {
	empty := TenantOwnerReceipt{}
	d, err := configuration.Parse(configJSON, manifestJSON)
	if err != nil {
		return empty, err
	}
	cfg := d.Config()
	base, err := url.Parse(cfg.OpenFGA.URL)
	if ctx == nil || err != nil || (base.Path != "" && base.Path != "/") || len(token) > 4096 ||
		!providerToken.MatchString(token) || !providerID.MatchString(input.OperationID) ||
		!providerID.MatchString(input.TenantID) || cfg.Issuer != input.Issuer || beforeWrite == nil {
		return empty, contract.ErrInvalidArgument
	}
	user, err := contract.EncodeIdentity(input.Issuer, input.Subject)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	profile := cfg.TransportProfile
	if profile == "" {
		profile = "production"
	}
	store, model := cfg.OpenFGA.StoreID, cfg.OpenFGA.ModelID
	manifest := d.Manifest()
	pinned, err := modelbinding.New(modelbinding.Config{URL: cfg.OpenFGA.URL, StoreID: store, ModelID: model, Profile: profile},
		manifest, contract.Binding{ProductID: cfg.ProductID, ManifestVersion: cfg.ManifestVersion, ClientKey: cfg.ClientKey, TransportProfile: profile}, d.Artifact(),
		func(context.Context) (string, error) { return token, nil })
	if err != nil {
		return empty, err
	}
	verified, err := pinned.Verify(ctx)
	if err != nil {
		return empty, err
	}
	role, err := verified.Role(manifest.OwnerRole)
	if err != nil {
		return empty, err
	}
	object := "workspace:" + input.TenantID
	owner, member := ownerTuple{user, object, role.Assignment}, ownerTuple{user, object, "member"}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	path := "/stores/" + store
	unavailable := contract.NewError(contract.Unavailable, input.OperationID)
	read := func(tuple ownerTuple, allUsers bool) (bool, error) {
		if allUsers {
			tuple.User = ""
		}
		var page struct {
			Tuples *[]struct {
				Key struct {
					User, Object, Relation string
					Condition              any
				}
			}
			Continuation string `json:"continuation_token"`
		}
		found, err := providerCall(ctx, client, cfg.OpenFGA.URL, token, path+"/read", map[string]any{
			"tuple_key": tuple, "page_size": 2, "consistency": "HIGHER_CONSISTENCY",
		}, &page, false)
		if err != nil {
			return false, err
		}
		if !found || page.Tuples == nil {
			return false, unavailable
		}
		for _, item := range *page.Tuples {
			if item.Key.Object != object || item.Key.Relation != tuple.Relation {
				return false, unavailable
			}
		}
		for _, item := range *page.Tuples {
			if allUsers && (len(*page.Tuples) > 1 || item.Key.User != user) {
				return false, contract.NewError(contract.Conflict, input.OperationID)
			}
			if item.Key.User != user || item.Key.Condition != nil {
				return false, unavailable
			}
		}
		if page.Continuation != "" || len(*page.Tuples) > 1 {
			return false, unavailable
		}
		return len(*page.Tuples) == 1, nil
	}
	missing := make([]ownerTuple, 0, 2)
	for i, tuple := range []ownerTuple{owner, member} {
		present, err := read(tuple, i == 0)
		if err != nil {
			return empty, err
		}
		if !present {
			missing = append(missing, tuple)
		}
	}
	if len(missing) > 0 {
		if err := runSetupCallback(ctx, beforeWrite); err != nil {
			var typed *contract.Error
			if errors.As(err, &typed) {
				return empty, contract.NewError(typed.Code, input.OperationID)
			}
			return empty, unavailable
		}
		unknown := contract.NewError(contract.OutcomeUnknown, input.OperationID)
		if contract.CheckCancellation(ctx) != nil {
			return empty, unknown
		}
		var response map[string]any
		found, err := providerCall(ctx, client, cfg.OpenFGA.URL, token, path+"/write", map[string]any{
			"authorization_model_id": model, "writes": map[string]any{"tuple_keys": missing, "on_duplicate": "ignore"},
		}, &response, false)
		if err != nil || !found || response == nil {
			return empty, unknown
		}
		for i, tuple := range []ownerTuple{owner, member} {
			present, err := read(tuple, i == 0)
			if err != nil || !present {
				return empty, unknown
			}
		}
	}
	return TenantOwnerReceipt{input.OperationID, input.TenantID, user, store, model}, nil
}
