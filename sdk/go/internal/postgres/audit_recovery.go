package postgres

import (
	"context"
	"database/sql"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type AuditRecoveryReceipt struct {
	Terminal       contract.AuditReceipt `json:"terminal"`
	Administration contract.AuditReceipt `json:"administration"`
}

// RecoverDecision requires an authenticated, authorized maintenance initiator.
func (a *Audit) RecoverDecision(ctx context.Context, terminal, administration map[string]any, cutoff int64) (*AuditRecoveryReceipt, error) {
	if ctx == nil {
		return nil, contract.ErrInvalidArgument
	}
	completion, e := a.prepare(terminal)
	if e != nil {
		return nil, e
	}
	recovery, e := a.prepare(administration)
	if e != nil {
		return nil, e
	}
	if e = contract.ValidateRecoveryPair(completion.snapshot, recovery.snapshot, cutoff); e != nil {
		return nil, e
	}
	receipt, e := a.appendFinalized(ctx, completion.snapshot, func(context.Context, *sql.Tx) error { return nil }, func(ctx context.Context, tx *sql.Tx, event map[string]any, _ bool) error {
		changed, e := a.persistDecision(ctx, tx, event, &cutoff)
		if e != nil {
			return e
		}
		_, e = a.writePrepared(ctx, tx, recovery, !changed)
		return e
	})
	if e != nil {
		return nil, e
	}
	return &AuditRecoveryReceipt{receipt, recovery.receipt}, nil
}
