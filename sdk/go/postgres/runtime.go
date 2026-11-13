// Package postgres composes PostgreSQL storage for the Heimdall runtime.
// Applications own their connection pools, migrations and delivery scheduling.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/internal/browserlogin"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type StorageOptions struct {
	ProductID, Environment string
	// AuditSinks fixes initial routing. An empty list stores events without delivery rows.
	AuditSinks []string
	Sessions   *SessionStorageOptions
}

type SessionStorageOptions struct {
	KeyUsageDB  *sql.DB
	ActiveKeyID string
	Keys        map[string][]byte
}

// NewStorage performs no I/O and never closes the caller's pool.
func NewStorage(db *sql.DB, options StorageOptions) (heimdall.RuntimeStorage, error) {
	empty := heimdall.RuntimeStorage{}
	identities, err := internal.NewRetainedRuntimeIdentityRecords(db, options.ProductID, options.Environment)
	if err != nil {
		return empty, err
	}
	audit, err := internal.NewRetainedRuntimeAudit(db, options.ProductID, options.Environment, options.AuditSinks, func(event map[string]any) error {
		if e := contract.ValidateAuditShape(event); e != nil {
			return e
		}
		raw, e := json.Marshal(event)
		if e != nil {
			return contract.ErrInvalidArgument
		}
		var typed contract.AuditEvent
		if json.Unmarshal(raw, &typed) != nil {
			return contract.ErrInvalidArgument
		}
		return contract.ValidateAuditEvent(typed, "sdk", func(string, int, map[string]any) bool { return false })
	})
	if err != nil {
		return empty, err
	}
	gate, err := identities.IdentityGate(options.ProductID, options.Environment)
	if err != nil {
		return empty, err
	}
	var browser *heimdall.BrowserStorage
	var keyDB *sql.DB
	if sessions := options.Sessions; sessions != nil {
		keyDB = sessions.KeyUsageDB
		cipher, err := contract.NewRecordCipher(sessions.ActiveKeyID, sessions.Keys)
		if err != nil {
			return empty, err
		}
		records, err := internal.NewRecords(db, keyDB, cipher, options.ProductID, options.Environment, nil)
		if err != nil {
			return empty, err
		}
		store, err := internal.NewSessionRecords(db, keyDB, cipher, options.ProductID, options.Environment, browserlogin.SessionPrincipal, nil)
		if err != nil {
			return empty, err
		}
		browser = &heimdall.BrowserStorage{Login: internal.NewBrowserLoginRecords(records, nil), Sessions: store}
	}
	return heimdall.RuntimeStorage{
		ProductID: options.ProductID, Environment: options.Environment, Browser: browser,
		Ready: func(ctx context.Context) error {
			if ctx == nil {
				return heimdall.ErrInvalidArgument
			}
			if err := internal.CheckRetentionReadiness(ctx, db); err != nil {
				return err
			}
			if keyDB != nil {
				return internal.CheckKeyUsageReadiness(ctx, keyDB)
			}
			return nil
		},
		Identity: heimdall.IdentityGate{
			Check: func(ctx context.Context, value heimdall.Identity) (bool, error) {
				if ctx == nil {
					return false, heimdall.ErrInvalidArgument
				}
				return gate.Check(ctx, value)
			},
			Invalidate: func(ctx context.Context, value heimdall.IdentityNegative) error {
				if ctx == nil {
					return heimdall.ErrInvalidArgument
				}
				return gate.Invalidate(ctx, value)
			},
		},
		AppendAudit: func(ctx context.Context, event map[string]any) (heimdall.AuditReceipt, error) {
			if ctx == nil {
				return heimdall.AuditReceipt{}, heimdall.ErrInvalidArgument
			}
			return audit.Append(ctx, event)
		},
	}, nil
}

type candidateSource struct {
	records *internal.CandidateRecords
	actor   contract.Identity
}

func (s candidateSource) Scan(ctx context.Context, options heimdall.ScanOptions) (heimdall.CandidatePage, error) {
	after := options.After
	if after == "" {
		after = "0"
	}
	page, err := s.records.Scan(ctx, s.actor, after, options.Limit)
	if err != nil {
		return heimdall.CandidatePage{}, err
	}
	items := make([]heimdall.Candidate, 0, len(page.Items))
	for _, candidate := range page.Items {
		items = append(items, heimdall.Candidate{Position: candidate.Position,
			Workspace: heimdall.Workspace{ID: candidate.Workspace.WorkspaceID, Name: candidate.Workspace.Name}})
	}
	return heimdall.CandidatePage{Items: items, Exhausted: page.NextPosition == nil}, nil
}

// NewManagedStorage adds mandatory workspace lifecycle checks and indexed discovery.
func NewManagedStorage(db *sql.DB, options StorageOptions) (heimdall.RuntimeStorage, error) {
	storage, err := NewStorage(db, options)
	if err != nil {
		return heimdall.RuntimeStorage{}, err
	}
	workspaces, err := internal.NewRetainedWorkspaceRecords(db, options.ProductID, options.Environment)
	if err != nil {
		return heimdall.RuntimeStorage{}, err
	}
	candidates, err := internal.NewRetainedCandidateRecords(db, options.ProductID, options.Environment)
	if err != nil {
		return heimdall.RuntimeStorage{}, err
	}
	ready := storage.Ready
	storage.Ready = func(ctx context.Context) error {
		if err := ready(ctx); err != nil {
			return err
		}
		return internal.CheckManagedRetentionReadiness(ctx, db)
	}
	sinks := append([]string{}, options.AuditSinks...)
	storage.Managed = &heimdall.ManagedStorage{
		Operations: func(binding internal.OperationBinding) (*internal.OperationRecords, error) {
			if binding.ProductID != options.ProductID || binding.Environment != options.Environment {
				return nil, heimdall.ErrInvalidArgument
			}
			return internal.NewRetainedOperationRecords(db, binding, sinks)
		},
		Allows: func(ctx context.Context, id string) (bool, error) {
			record, err := workspaces.Get(ctx, id)
			if err != nil || record == nil {
				return false, err
			}
			result, err := contract.WorkspaceGate(&record.WorkspaceRecord, contract.WorkspaceScope{
				ProductID: options.ProductID, Environment: options.Environment, WorkspaceID: id,
			})
			return result == "check_relationships", err
		},
		Source: func(actor heimdall.Identity) heimdall.WorkspaceSource {
			return candidateSource{candidates, contract.Identity{Issuer: actor.Issuer, Subject: actor.Subject}}
		},
	}
	return storage, nil
}
