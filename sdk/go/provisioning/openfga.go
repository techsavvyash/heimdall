package provisioning

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/modelbinding"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type openFGAOptions struct {
	scope                            postgres.SetupScope
	configJSON, manifestJSON         []byte
	token                            string
	existingStoreID, existingModelID string
	recoverStoreID, recoverModelID   string
}

func provisionOpenFGA(ctx context.Context, db *sql.DB, o openFGAOptions) (modelbinding.Config, error) {
	empty := modelbinding.Config{}
	deployment, err := configuration.Parse(o.configJSON, o.manifestJSON)
	if err != nil {
		return empty, err
	}
	cfg := deployment.Config()
	manifest := deployment.Manifest()
	profile := cfg.TransportProfile
	if profile == "" {
		profile = "production"
	}
	base := cfg.OpenFGA.URL
	parsed, err := url.Parse(base)
	if ctx == nil || db == nil || err != nil || (parsed.Path != "" && parsed.Path != "/") ||
		cfg.ProductID != o.scope.ProductID || cfg.Environment != o.scope.Environment ||
		o.scope.StepCount != len(manifest.Clients)+3 || len(o.token) > 4096 || !providerToken.MatchString(o.token) ||
		(o.existingModelID != "" && o.existingStoreID == "") || (o.existingStoreID != "" && o.recoverStoreID != "") ||
		(o.existingModelID != "" && o.recoverModelID != "") {
		return empty, contract.ErrInvalidArgument
	}
	for _, id := range []string{o.existingStoreID, o.existingModelID, o.recoverStoreID, o.recoverModelID} {
		if id != "" && !providerID.MatchString(id) {
			return empty, contract.ErrInvalidArgument
		}
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	claim := func(ordinal int, recover string) (postgres.SetupStep, error) {
		if recover != "" {
			if err := postgres.RequireSetupDispatch(ctx, db, o.scope, ordinal); err != nil {
				return postgres.SetupStep{}, err
			}
		}
		id, err := newDispatchID()
		if err != nil {
			return postgres.SetupStep{}, err
		}
		return postgres.ClaimSetupStep(ctx, db, o.scope, ordinal, id)
	}
	storeOrdinal := o.scope.StepCount - 1
	store, err := claim(storeOrdinal, o.recoverStoreID)
	if err != nil {
		return empty, err
	}
	name := "heimdall-" + store.DispatchID
	storeID, err := setupResourceID(store, o.existingStoreID, o.recoverStoreID, func() (string, error) {
		var created struct{ ID string }
		_, err := providerCall(ctx, client, base, o.token, "/stores", map[string]string{"name": name}, &created, false)
		return created.ID, err
	})
	if err != nil {
		return empty, err
	}
	var observed struct{ ID, Name string }
	found, err := providerCall(ctx, client, base, o.token, "/stores/"+storeID, nil, &observed, false)
	if err != nil {
		return empty, err
	}
	if !found || observed.ID != storeID || (o.existingStoreID == "" && observed.Name != name) {
		return empty, contract.NewError(contract.Conflict, store.DispatchID)
	}
	if store.State != "confirmed" {
		if err := postgres.ConfirmSetupStep(ctx, db, o.scope, storeOrdinal, store.DispatchID, storeID); err != nil {
			return empty, err
		}
	}
	model, err := claim(o.scope.StepCount, o.recoverModelID)
	if err != nil {
		return empty, err
	}
	artifact := deployment.Artifact()
	modelID, err := setupResourceID(model, o.existingModelID, o.recoverModelID, func() (string, error) {
		var created struct {
			ID string `json:"authorization_model_id"`
		}
		_, err := providerCall(ctx, client, base, o.token, "/stores/"+storeID+"/authorization-models", artifact.Model, &created, false)
		return created.ID, err
	})
	if err != nil {
		return empty, err
	}
	binding := modelbinding.Config{URL: base, StoreID: storeID, ModelID: modelID, Profile: profile}
	pinned, err := modelbinding.New(binding, manifest, contract.Binding{ProductID: cfg.ProductID, ManifestVersion: cfg.ManifestVersion, ClientKey: cfg.ClientKey, TransportProfile: profile}, artifact, func(context.Context) (string, error) { return o.token, nil })
	if err != nil {
		return empty, err
	}
	if _, err = pinned.Verify(ctx); err != nil {
		return empty, err
	}
	if model.State != "confirmed" {
		if err := postgres.ConfirmSetupStep(ctx, db, o.scope, o.scope.StepCount, model.DispatchID, modelID); err != nil {
			return empty, err
		}
	}
	return binding, nil
}

func setupResourceID(step postgres.SetupStep, existing, recover string, create func() (string, error)) (string, error) {
	conflict := contract.NewError(contract.Conflict, step.DispatchID)
	unknown := contract.NewError(contract.OutcomeUnknown, step.DispatchID)
	if step.State == "claimed" && recover != "" {
		return "", conflict
	}
	id := step.ResourceID
	if id == "" {
		id = existing
	}
	if id == "" {
		id = recover
	}
	if id == "" && step.State == "claimed" {
		var err error
		id, err = create()
		if err != nil {
			return "", unknown
		}
	}
	if !providerID.MatchString(id) {
		return "", unknown
	}
	if (existing != "" && existing != id) || (recover != "" && recover != id) {
		return "", conflict
	}
	return id, nil
}
