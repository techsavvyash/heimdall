package provisioning

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/modelbinding"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type WorkspaceRecoveryScope struct {
	ProductID, Environment, OperationID, WorkspaceID, StoreID, ModelID string
	Kind                                                               string
	Generation, UpdatedAt                                              int64
}

type WorkspaceRecoveryEvidence struct{ OperatorID, EvidenceID string }

type WorkspaceRecoveryOptions struct {
	OperationID, InitiatingIssuer, InitiatingSubject, PolicyVersion, RecoveryToken string
	ExpectedGeneration                                                             int64
	AuditSinks                                                                     []string
	// VerifyQuiescence authorizes the operator and verifies quiescence of every old writer for this generation.
	VerifyQuiescence func(context.Context, WorkspaceRecoveryScope) (WorkspaceRecoveryEvidence, error)
}

type WorkspaceRecoveryResult struct {
	OperationID string `json:"operationId"`
	WorkspaceID string `json:"workspaceId"`
	Status      string `json:"status"`
}

// RecoverWorkspaceCreation requires a trusted maintenance caller to keep the deployment drained throughout.
func RecoverWorkspaceCreation(ctx context.Context, db *sql.DB, configJSON, manifestJSON []byte, options WorkspaceRecoveryOptions) (WorkspaceRecoveryResult, error) {
	return recoverOperation(ctx, db, configJSON, manifestJSON, options, true)
}

// RecoverMembership completes an audited add, role replacement or removal under operator quiescence.
func RecoverMembership(ctx context.Context, db *sql.DB, configJSON, manifestJSON []byte, options WorkspaceRecoveryOptions) (WorkspaceRecoveryResult, error) {
	return recoverOperation(ctx, db, configJSON, manifestJSON, options, false)
}

func recoverOperation(ctx context.Context, db *sql.DB, configJSON, manifestJSON []byte, options WorkspaceRecoveryOptions, creating bool) (WorkspaceRecoveryResult, error) {
	empty := WorkspaceRecoveryResult{}
	configJSON, manifestJSON = append([]byte(nil), configJSON...), append([]byte(nil), manifestJSON...)
	d, err := configuration.Parse(configJSON, manifestJSON)
	if err != nil {
		return empty, err
	}
	cfg := d.Config()
	id := options.OperationID
	if ctx == nil || !providerID.MatchString(id) || !providerID.MatchString(options.PolicyVersion) ||
		options.ExpectedGeneration < 1 || options.ExpectedGeneration > 9007199254740991 ||
		options.InitiatingIssuer != cfg.Issuer || options.VerifyQuiescence == nil {
		return empty, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	binding := postgres.OperationBinding{
		ProductID: cfg.ProductID, Environment: cfg.Environment, StoreID: cfg.OpenFGA.StoreID,
		ModelID: cfg.OpenFGA.ModelID, ModelHash: d.Artifact().ModelHash, ManifestVersion: cfg.ManifestVersion,
	}
	records, err := postgres.NewRetainedOperationRecords(db, binding, options.AuditSinks)
	if err != nil {
		return empty, err
	}
	actor := contract.Identity{Issuer: options.InitiatingIssuer, Subject: options.InitiatingSubject}
	op, err := records.Get(ctx, id, actor)
	if err != nil {
		return empty, err
	}
	if op == nil || (creating && op.Intent.Kind != "create_workspace") ||
		(!creating && op.Intent.Kind != "add_member" && op.Intent.Kind != "assign_roles" && op.Intent.Kind != "remove_member") {
		return empty, contract.ErrNotFound
	}
	conflict := contract.NewError(contract.Conflict, id)
	if op.Generation != options.ExpectedGeneration || (op.State != "uncertain" && op.State != "dispatching") {
		return empty, conflict
	}
	scope := WorkspaceRecoveryScope{
		ProductID: cfg.ProductID, Environment: cfg.Environment, OperationID: id, WorkspaceID: op.WorkspaceID,
		StoreID: binding.StoreID, ModelID: binding.ModelID, Generation: op.Generation, UpdatedAt: op.UpdatedAt, Kind: op.Intent.Kind,
	}
	proofs := make(chan WorkspaceRecoveryEvidence, 1)
	if err := runSetupCallback(ctx, func(operation context.Context) error {
		proof, err := options.VerifyQuiescence(operation, scope)
		proofs <- proof
		return err
	}); err != nil {
		var typed *contract.Error
		if errors.As(err, &typed) {
			return empty, contract.NewError(typed.Code, "")
		}
		return empty, contract.ErrUnavailable
	}
	proof := <-proofs
	if !providerID.MatchString(proof.OperatorID) || !providerID.MatchString(proof.EvidenceID) {
		return empty, contract.ErrForbidden
	}
	advance := func(transition string) error {
		at := max(time.Now().UnixMilli(), op.UpdatedAt)
		var event [16]byte
		rand.Read(event[:])
		next, err := records.Advance(ctx, id, actor, op.Generation, transition, postgres.OperationTransitionMetadata{
			EventID: hex.EncodeToString(event[:]), RequestID: proof.EvidenceID, PolicyVersion: options.PolicyVersion,
			OccurredAt: at, RecordedAt: at, NextAttemptAt: at, RecoveryOperator: proof.OperatorID,
		})
		if err == nil {
			op = next
		}
		return err
	}
	// Reserve the new generation before any provider I/O. A lost claim response cannot dispatch.
	if err := advance("recover"); err != nil {
		return empty, err
	}
	status := "confirmed"
	if creating {
		status = "active"
		target := op.Intent.Target
		_, err = EnsureTenantOwner(ctx, configJSON, manifestJSON, options.RecoveryToken, TenantOwnerInput{
			OperationID: id, TenantID: op.WorkspaceID, Issuer: target.Issuer, Subject: target.Subject,
		}, contract.CheckCancellation)
	} else {
		profile := cfg.TransportProfile
		if profile == "" {
			profile = "production"
		}
		manifest := d.Manifest()
		model, modelErr := modelbinding.New(modelbinding.Config{URL: cfg.OpenFGA.URL, StoreID: binding.StoreID, ModelID: binding.ModelID, Profile: profile},
			manifest, contract.Binding{ProductID: cfg.ProductID, ManifestVersion: cfg.ManifestVersion, ClientKey: cfg.ClientKey, TransportProfile: profile},
			d.Artifact(), func(context.Context) (string, error) { return options.RecoveryToken, nil })
		err = modelErr
		if err == nil {
			var apply func(context.Context, string) error
			apply, err = model.PlanMembershipRecovery(ctx, manifest.OwnerRole, op.Intent)
			if err == nil {
				err = apply(ctx, id)
			}
		}
	}
	if err != nil {
		if errors.Is(err, contract.ErrConflict) || errors.Is(err, contract.ErrForbidden) || errors.Is(err, contract.ErrNotFound) {
			return empty, conflict
		}
		return empty, contract.NewError(contract.OutcomeUnknown, id)
	}
	for _, transition := range []string{"confirm", "complete"} {
		if err := advance(transition); err != nil {
			return empty, err
		}
	}
	return WorkspaceRecoveryResult{id, op.WorkspaceID, status}, nil
}
