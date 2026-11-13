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
