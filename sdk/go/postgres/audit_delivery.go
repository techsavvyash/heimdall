package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	internal "github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type AuditDeliveryOptions struct {
	ProductID, Environment, SinkID    string
	Endpoint, Token, TransportProfile string
	ValidateCustom                    CustomAuditValidator
}

type AuditDeliveryResult struct {
	Attempted, Delivered, Quarantined int
}

type AuditDelivery struct {
	audit   *internal.RetainedRuntimeAudit
	options AuditDeliveryOptions
	client  *http.Client
}

type auditBatch struct {
	ProductID   string           `json:"productId"`
	Environment string           `json:"environment"`
	SinkID      string           `json:"sinkId"`
	EventIDs    []string         `json:"eventIds"`
	Events      []map[string]any `json:"events,omitempty"`
}

var auditBearer = regexp.MustCompile(`^[!-~]+$`)

func NewAuditDelivery(pool *sql.DB, options AuditDeliveryOptions) (*AuditDelivery, error) {
	profile := options.TransportProfile
	if profile == "" {
		profile = "production"
	}
	if len(options.Token) > 4096 || !auditBearer.MatchString(options.Token) || contract.ValidateEndpoint(options.Endpoint, profile) != nil {
		return nil, contract.ErrInvalidArgument
	}
	audit, err := internal.NewRetainedRuntimeAudit(pool, options.ProductID, options.Environment, []string{options.SinkID}, auditEventValidator(options.ValidateCustom, false))
	if err != nil {
		return nil, err
	}
	return &AuditDelivery{audit, options, &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type claimedAudit struct {
	item    internal.PendingAudit
	attempt int
}

func (d *AuditDelivery) Deliver(ctx context.Context, limit int) (AuditDeliveryResult, error) {
	result := AuditDeliveryResult{}
	if d == nil || d.audit == nil || ctx == nil {
		return result, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sink := d.options.SinkID
	items, err := d.audit.Pending(ctx, sink, time.Now().UnixMilli(), limit)
	if err != nil {
		return result, err
	}
	claimed := make([]claimedAudit, 0, len(items))
	for _, item := range items {
		if item.Event == nil || item.Attempts == 2147483647 {
			changed, err := d.audit.Quarantine(ctx, sink, item.EventID, item.Attempts)
			if err != nil {
				return result, err
			}
			if changed {
				result.Quarantined++
			}
			continue
		}
		now := time.Now().UnixMilli()
		delay := min(int64(300000), int64(60000)<<min(item.Attempts, 3))
		attempt, err := d.audit.BeginAttempt(ctx, sink, item.EventID, item.Attempts, now, now+delay+rand.Int64N(5000))
		if err != nil {
			return result, err
		}
		if attempt != nil {
			claimed = append(claimed, claimedAudit{item, *attempt})
		}
	}
	result.Attempted = len(claimed)
	if len(claimed) != 0 {
		err = d.send(ctx, claimed, &result)
	}
	return result, err
}

func (d *AuditDelivery) send(ctx context.Context, items []claimedAudit, result *AuditDeliveryResult) error {
	scope := d.options
	batch := auditBatch{ProductID: scope.ProductID, Environment: scope.Environment, SinkID: scope.SinkID}
	for _, item := range items {
		batch.EventIDs = append(batch.EventIDs, item.item.EventID)
		batch.Events = append(batch.Events, item.item.Event)
	}
	status, receipt, err := d.post(ctx, batch)
	if err != nil {
		return err
	}
	if status == http.StatusUnprocessableEntity {
		if len(items) == 1 {
			item := items[0]
			changed, err := d.audit.Quarantine(ctx, scope.SinkID, item.item.EventID, item.attempt)
			if changed {
				result.Quarantined++
			}
			return err
		}
		middle := len(items) / 2
		if err := d.send(ctx, items[:middle], result); err != nil {
			return err
		}
		return d.send(ctx, items[middle:], result)
	}
	err = d.audit.Acknowledge(ctx,
		contract.AuditDelivery{ProductID: batch.ProductID, Environment: batch.Environment, SinkID: batch.SinkID, EventIDs: batch.EventIDs},
		contract.AuditDelivery{ProductID: receipt.ProductID, Environment: receipt.Environment, SinkID: receipt.SinkID, EventIDs: receipt.EventIDs},
		time.Now().UnixMilli())
	if err == nil {
		result.Delivered += len(receipt.EventIDs)
	}
	return err
}

func (d *AuditDelivery) post(ctx context.Context, batch auditBatch) (int, auditBatch, error) {
	var receipt auditBatch
	payload, err := json.Marshal(batch)
	if err != nil {
		return 0, receipt, contract.ErrUnavailable
	}
	request, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(request, "POST", d.options.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, receipt, contract.ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.options.Token)
	response, err := d.client.Do(req)
	if err != nil {
		if stopped := contract.CheckCancellation(request); stopped != nil {
			return 0, receipt, stopped
		}
		return 0, receipt, contract.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnprocessableEntity {
		return response.StatusCode, receipt, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, receipt, contract.ErrUnavailable
	}
	const maxReceipt = 32768
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxReceipt+1))
	if stopped := contract.CheckCancellation(request); stopped != nil {
		return 0, receipt, stopped
	}
	if err != nil || len(raw) > maxReceipt || json.Unmarshal(raw, &receipt) != nil {
		return 0, receipt, contract.ErrUnavailable
	}
	return response.StatusCode, receipt, nil
}

func (d *AuditDelivery) Requeue(ctx context.Context, eventID string, attempts int) (bool, error) {
	if d == nil || d.audit == nil || ctx == nil {
		return false, contract.ErrInvalidArgument
	}
	return d.audit.RequeueQuarantined(ctx, d.options.SinkID, eventID, attempts, time.Now().UnixMilli())
}
