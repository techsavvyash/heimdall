// Package postgres composes PostgreSQL storage for the Heimdall runtime.
// Applications own their connection pools, migrations and delivery scheduling.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type StorageOptions struct {
	ProductID, Environment string
	// AuditSinks fixes initial routing. An empty list stores events without delivery rows.
	AuditSinks []string
}

// NewStorage performs no I/O and never closes the caller's pool.
func NewStorage(db *sql.DB, options StorageOptions) (heimdall.RuntimeStorage, error) {
	empty := heimdall.RuntimeStorage{}
	identities, err := internal.NewIdentityRecords(db, options.ProductID, options.Environment, nil)
	if err != nil {
		return empty, err
	}
	audit, err := internal.NewAudit(db, options.ProductID, options.Environment, options.AuditSinks, func(event map[string]any) error {
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
	return heimdall.RuntimeStorage{
		ProductID: options.ProductID, Environment: options.Environment,
		Ready: func(ctx context.Context) error {
			if ctx == nil {
				return heimdall.ErrInvalidArgument
			}
			return internal.CheckReadiness(ctx, db)
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
