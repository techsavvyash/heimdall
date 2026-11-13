package heimdall

import (
	"context"
	"errors"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type ReconcileOptions struct {
	// Zero uses the default of 100.
	MaxOperations int
	After         *string
}

type ReconcileOperation struct {
	OperationID string `json:"operationId"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason,omitempty"`
}

type ReconcileResult struct {
	Attempted  int                  `json:"attempted"`
	Completed  int                  `json:"completed"`
	Retryable  int                  `json:"retryable"`
	Blocked    int                  `json:"blocked"`
	Next       *string              `json:"next"`
	Operations []ReconcileOperation `json:"operations"`
}

// Reconcile is a trusted product scheduler entry point for managed operations.
func (c *Client) Reconcile(ctx context.Context, options ReconcileOptions) (ReconcileResult, error) {
	return c.Onboarding().Reconcile(ctx, options)
}

func (w *WorkspaceOnboarding) Reconcile(ctx context.Context, options ReconcileOptions) (ReconcileResult, error) {
	empty := ReconcileResult{}
	limit := options.MaxOperations
	if limit == 0 {
		limit = 100
	}
	if ctx == nil || w == nil || limit < 1 || limit > 1000 ||
		(options.After != nil && !tenantID.MatchString(*options.After)) {
		return empty, ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := ReconcileResult{Next: options.After, Operations: make([]ReconcileOperation, 0, limit)}
	for {
		page, err := w.records.Scan(ctx, "reconcile", result.Next, time.Now().UnixMilli(), min(100, limit-len(result.Operations)))
		if err != nil {
			return empty, err
		}
		for _, op := range page.Items {
			if err := contract.CheckCancellation(ctx); err != nil {
				return empty, err
			}
			item := ReconcileOperation{OperationID: op.OperationID, Disposition: "blocked", Reason: "recovery_required"}
			switch {
			case op.State == "dispatching" || op.State == "uncertain":
			case op.State != "confirmed" && op.Intent.Kind != "create_workspace":
				item.Reason = "initiator_required"
			default:
				result.Attempted++
				status, err := w.drive(ctx, op, true)
				if err != nil {
					var known *contract.Error
					item.Reason = "unavailable"
					if errors.As(err, &known) {
						item.Reason = string(known.Code)
					}
					switch item.Reason {
					case "unavailable", "audit_unavailable", "deadline_exceeded", "conflict":
						item.Disposition = "retryable"
					}
				} else {
					item.Reason = status.Reason
					switch {
					case status.Status == "active":
						item.Disposition = "completed"
					case item.Reason == "retryable":
						item.Disposition = "retryable"
					}
				}
			}
			switch item.Disposition {
			case "completed":
				result.Completed++
			case "retryable":
				result.Retryable++
			case "blocked":
				result.Blocked++
			}
			result.Operations = append(result.Operations, item)
		}
		if err := contract.CheckCancellation(ctx); err != nil {
			return empty, err
		}
		result.Next = page.NextOperationID
		if result.Next == nil || len(result.Operations) == limit {
			return result, nil
		}
	}
}
