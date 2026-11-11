package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

type recoveryPort interface {
	ScanPendingDecisions(context.Context, int64, *AuditDecisionCursor, int) (*AuditDecisionPage, error)
	RecoverRuntime(context.Context, map[string]any, map[string]any, int64) (*AuditRecoveryReceipt, error)
	ReadResolution(context.Context, string) (*AuditResolution, error)
}
type RecoveryObservation struct {
	DecisionID       string  `json:"decisionId"`
	State            string  `json:"state"`
	TerminalID       string  `json:"terminalId"`
	AdministrationID *string `json:"administrationId"`
}
type RecoveryBatch struct {
	Observed []RecoveryObservation `json:"observed"`
	More     bool                  `json:"more"`
}

// RecoverPendingDecisions is trusted maintenance; propose must authorize its initiator.
func RecoverPendingDecisions(ctx context.Context, port recoveryPort, cutoff int64, limit int, propose func(map[string]any) (*contract.RecoveryProposal, error)) (*RecoveryBatch, error) {
	if ctx == nil || port == nil || propose == nil || cutoff < 0 || cutoff > maxSafe || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	// Terminal slots leave the durable pending queue; no external cursor is advanced.
	page, e := port.ScanPendingDecisions(ctx, cutoff, nil, limit)
	if e != nil {
		return nil, e
	}
	result := &RecoveryBatch{Observed: []RecoveryObservation{}, More: page.Next != nil}
	for _, candidate := range page.Items {
		if e = contract.CheckCancellation(ctx); e != nil {
			return nil, e
		}
		raw, e := json.Marshal(candidate.Request)
		if e != nil {
			return nil, contract.ErrAuditUnavailable
		}
		var original, copy map[string]any
		if json.Unmarshal(raw, &original) != nil || json.Unmarshal(raw, &copy) != nil {
			return nil, contract.ErrAuditUnavailable
		}
		proposal, e := propose(copy)
		if e != nil {
			return nil, e
		}
		encoded, e := json.Marshal(proposal)
		if e != nil {
			return nil, contract.ErrInvalidArgument
		}
		proposal, e = contract.DecodeRecoveryProposal(original, encoded)
		if e != nil {
			return nil, e
		}
		if proposal.Cutoff != cutoff {
			return nil, contract.ErrInvalidArgument
		}
		if e = contract.CheckCancellation(ctx); e != nil {
			return nil, e
		}
		_, writeErr := port.RecoverRuntime(ctx, proposal.Terminal, proposal.Administration, cutoff)
		if writeErr != nil && !errors.Is(writeErr, contract.ErrConflict) && !errors.Is(writeErr, contract.ErrOutcomeUnknown) {
			return nil, writeErr
		}
		resolution, e := port.ReadResolution(ctx, candidate.DecisionID)
		if e != nil {
			if errors.Is(writeErr, contract.ErrOutcomeUnknown) {
				return nil, writeErr
			}
			return nil, e
		}
		if resolution == nil || resolution.State == "pending" {
			if writeErr != nil {
				return nil, writeErr
			}
			return nil, contract.ErrAuditUnavailable
		}
		terminal, ok := resolution.Terminal["eventId"].(string)
		if !ok {
			return nil, contract.ErrAuditUnavailable
		}
		var administration *string
		if resolution.Administration != nil {
			id, ok := resolution.Administration["eventId"].(string)
			if !ok {
				return nil, contract.ErrAuditUnavailable
			}
			administration = &id
		}
		result.Observed = append(result.Observed, RecoveryObservation{candidate.DecisionID, resolution.State, terminal, administration})
	}
	return result, nil
}
