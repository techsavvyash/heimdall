package heimdall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

type WorkspaceCreateInput struct{ Name, IdempotencyKey string }
type WorkspaceCreationPolicy func(ctx context.Context, actor *Principal, name string) (bool, error)
type WorkspaceOnboardingStatus struct {
	OperationID string `json:"operationId"`
	WorkspaceID string `json:"workspaceId"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
}

type WorkspaceOnboarding struct {
	config, manifest []byte
	cfg              configuration.Config
	records          *postgres.OperationRecords
	auth             *auditflow.Authentication
	policy           WorkspaceCreationPolicy
	policyVersion    string
	key              func(context.Context) (string, error)
}

// Onboarding exposes tenant creation for managed PostgreSQL runtimes.
func (c *Client) Onboarding() *WorkspaceOnboarding {
	if c == nil {
		return nil
	}
	return c.onboarding
}

func onboardingID() string {
	var bytes [16]byte
	rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func onboardingStatus(op *postgres.ManagedOperation) WorkspaceOnboardingStatus {
	result := WorkspaceOnboardingStatus{OperationID: op.OperationID, WorkspaceID: op.WorkspaceID, Status: "pending"}
	switch op.State {
	case "completed":
		result.Status = "active"
	case "cancelled":
		result.Status = "cancelled"
	case "dispatching", "uncertain":
		result.Reason = "recovery_required"
	case "retryable":
		result.Reason = "retryable"
	}
	return result
}

func (w *WorkspaceOnboarding) identity(ctx context.Context, actor *Principal) (contract.Identity, error) {
	if w == nil || w.auth == nil || ctx == nil {
		return contract.Identity{}, ErrInvalidArgument
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return contract.Identity{}, err
	}
	if !w.auth.Owns(actor) {
		return contract.Identity{}, ErrUnauthenticated
	}
	id := actor.Identity()
	return contract.Identity{Issuer: id.Issuer, Subject: id.Subject}, nil
}

func (w *WorkspaceOnboarding) Create(ctx context.Context, actor *Principal, input WorkspaceCreateInput) (WorkspaceOnboardingStatus, error) {
	empty := WorkspaceOnboardingStatus{}
	id, err := w.identity(ctx, actor)
	if err != nil {
		return empty, err
	}
	intent := contract.Intent{
		SchemaVersion: 1, Kind: "create_workspace", ProductID: w.cfg.ProductID, Environment: w.cfg.Environment,
		ManifestVersion: w.cfg.ManifestVersion, Actor: id, Target: id, Name: input.Name, Roles: []string{},
	}
	if _, err = contract.IntentDigests(intent, input.IdempotencyKey); err != nil {
		return empty, err
	}
	var existing *postgres.ManagedOperation
	allowed, err := w.auth.CheckPermission(ctx, actor, "", "workspace.create", w.cfg.OpenFGA.ModelID, w.cfg.ManifestVersion,
		func(operation context.Context) (bool, error) {
			var err error
			existing, err = w.records.FindCreate(operation, intent, input.IdempotencyKey)
			if err != nil || existing != nil || w.policy == nil {
				return existing != nil, err
			}
			return w.policy(operation, actor, input.Name)
		})
	if err != nil {
		return empty, err
	}
	if !allowed {
		return empty, ErrForbidden
	}
	if existing != nil {
		return onboardingStatus(existing), nil
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return empty, ErrInvalidArgument
	}
	idempotentID, at := onboardingID(), time.Now().UnixMilli()
	op, err := w.records.Create(ctx, raw, input.IdempotencyKey, postgres.OperationAllocation{
		WorkspaceID: onboardingID(), OperationID: idempotentID, EventID: onboardingID(),
		RequestID: onboardingID(), PolicyVersion: w.policyVersion, OccurredAt: at, RecordedAt: at,
	})
	if err != nil {
		return empty, err
	}
	if op.OperationID != idempotentID {
		return onboardingStatus(op), nil
	}
	return w.drive(ctx, op)
}

func (w *WorkspaceOnboarding) observe(ctx context.Context, actor *Principal, operationID, action string) (*postgres.ManagedOperation, error) {
	id, err := w.identity(ctx, actor)
	if err != nil {
		return nil, err
	}
	var result *postgres.ManagedOperation
	allowed, err := w.auth.CheckPermission(ctx, actor, "", action, w.cfg.OpenFGA.ModelID, w.cfg.ManifestVersion,
		func(operation context.Context) (bool, error) {
			record, err := w.records.Get(operation, operationID, id)
			if err != nil || record == nil || record.Intent.Kind != "create_workspace" {
				return false, err
			}
			result = record
			return true, nil
		})
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotFound
	}
	return result, nil
}

func (w *WorkspaceOnboarding) Get(ctx context.Context, actor *Principal, operationID string) (WorkspaceOnboardingStatus, error) {
	record, err := w.observe(ctx, actor, operationID, "workspace.operation.read")
	if err != nil {
		return WorkspaceOnboardingStatus{}, err
	}
	return onboardingStatus(record), nil
}

func (w *WorkspaceOnboarding) Resume(ctx context.Context, actor *Principal, operationID string) (WorkspaceOnboardingStatus, error) {
	record, err := w.observe(ctx, actor, operationID, "workspace.operation.resume")
	if err != nil {
		return WorkspaceOnboardingStatus{}, err
	}
	return w.drive(ctx, record)
}

func (w *WorkspaceOnboarding) drive(ctx context.Context, op *postgres.ManagedOperation) (WorkspaceOnboardingStatus, error) {
	actor, requestID := op.Intent.Actor, onboardingID()
	advance := func(operation context.Context, before *postgres.ManagedOperation, transition string) (*postgres.ManagedOperation, error) {
		at := max(time.Now().UnixMilli(), before.UpdatedAt)
		return w.records.Advance(operation, before.OperationID, actor, before.Generation, transition, postgres.OperationTransitionMetadata{
			EventID: onboardingID(), RequestID: requestID, PolicyVersion: w.policyVersion,
			OccurredAt: at, RecordedAt: at, NextAttemptAt: at,
		})
	}
	change := func(transition string) error {
		next, err := advance(ctx, op, transition)
		if err == nil {
			op = next
		}
		return err
	}
	resolve := func(err error) (WorkspaceOnboardingStatus, error) {
		if errors.Is(err, ErrConflict) && ctx.Err() == nil {
			current, readErr := w.records.Get(ctx, op.OperationID, actor)
			if readErr != nil {
				return WorkspaceOnboardingStatus{}, readErr
			}
			if current != nil && current.Generation != op.Generation {
				return onboardingStatus(current), nil
			}
		}
		return WorkspaceOnboardingStatus{}, err
	}
	if op.State == "retryable" {
		if time.Now().UnixMilli() < op.NextAttemptAt {
			return onboardingStatus(op), nil
		}
		if err := change("retry"); err != nil {
			return resolve(err)
		}
	}
	if op.State == "pending" {
		before := op
		var started atomic.Bool
		var dispatched atomic.Pointer[postgres.ManagedOperation]
		dispatch := func(operation context.Context) error {
			started.Store(true)
			next, err := advance(operation, before, "dispatch")
			if err == nil {
				dispatched.Store(next)
			}
			return err
		}
		token, err := auditflow.LoadCredential(ctx, w.key)
		if err == nil {
			target := before.Intent.Target
			_, err = provisioning.EnsureTenantOwner(ctx, w.config, w.manifest, token, provisioning.TenantOwnerInput{
				OperationID: before.OperationID, TenantID: before.WorkspaceID, Issuer: target.Issuer, Subject: target.Subject,
			}, dispatch)
		}
		if next := dispatched.Load(); next != nil {
			op = next
		}
		if err != nil {
			if errors.Is(err, ErrOutcomeUnknown) && op.State == "dispatching" {
				if ctx.Err() == nil {
					if err := change("uncertain"); err != nil {
						return resolve(err)
					}
				}
				return onboardingStatus(op), nil
			}
			if !started.Load() && ctx.Err() == nil && (errors.Is(err, ErrUnavailable) || errors.Is(err, ErrDeadlineExceeded)) {
				if err := change("pre_dispatch_failure"); err != nil {
					return resolve(err)
				}
				return onboardingStatus(op), nil
			}
			return resolve(err)
		}
		// A verification-only attempt still needs its own generation before confirmation.
		if !started.Load() {
			if err := change("dispatch"); err != nil {
				return resolve(err)
			}
		}
		if err := change("confirm"); err != nil {
			return resolve(err)
		}
	}
	if op.State == "confirmed" {
		if err := change("complete"); err != nil {
			return resolve(err)
		}
	}
	return onboardingStatus(op), nil
}
