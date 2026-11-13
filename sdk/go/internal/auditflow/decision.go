package auditflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"regexp"
	"time"
)

type Append func(context.Context, map[string]any) (contract.AuditReceipt, error)
type Decision struct {
	product, environment string
	append               Append
	validate             func(map[string]any) error
	now                  func() time.Time
}

func New(product, environment string, append Append, validate func(map[string]any) error, now func() time.Time) (*Decision, error) {
	valid := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	if !valid.MatchString(product) || !valid.MatchString(environment) || append == nil || validate == nil {
		return nil, contract.ErrInvalidArgument
	}
	if now == nil {
		now = time.Now
	}
	return &Decision{product, environment, append, validate, now}, nil
}
func snapshot(event map[string]any) (map[string]any, error) {
	if _, e := contract.AuditDigest(event); e != nil {
		return nil, e
	}
	raw, e := json.Marshal(event)
	if e != nil {
		return nil, contract.ErrInvalidArgument
	}
	var copy map[string]any
	e = json.Unmarshal(raw, &copy)
	return copy, e
}
func bounded[T any](ctx context.Context, action func(context.Context) (T, error)) (T, error) {
	var zero T
	if e := contract.CheckCancellation(ctx); e != nil {
		return zero, e
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		if e := contract.CheckCancellation(ctx); e != nil {
			done <- result{zero, e}
			return
		}
		value, e := action(ctx)
		done <- result{value, e}
	}()
	select {
	case <-ctx.Done():
		return zero, contract.CheckCancellation(ctx)
	case r := <-done:
		return r.value, r.err
	}
}
func (d *Decision) valid(event map[string]any) error {
	if e := d.validate(event); e != nil {
		return e
	}
	raw, e := json.Marshal(event)
	if e != nil {
		return e
	}
	var typed contract.AuditEvent
	if e = json.Unmarshal(raw, &typed); e != nil {
		return e
	}
	return contract.ValidateAuditEvent(typed, "sdk", func(string, int, map[string]any) bool { return false })
}
func (d *Decision) commit(event map[string]any) error {
	if d.valid(event) != nil {
		return contract.ErrAuditUnavailable
	}
	digest, e := contract.AuditDigest(event)
	if e != nil {
		return contract.ErrAuditUnavailable
	}
	copy, e := snapshot(event)
	if e != nil {
		return contract.ErrAuditUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receipt, e := bounded(ctx, func(ctx context.Context) (contract.AuditReceipt, error) { return d.append(ctx, copy) })
	if e != nil {
		return contract.ErrAuditUnavailable
	}
	expected := contract.AuditReceipt{ProductID: event["productId"].(string), Environment: event["environment"].(string), EventID: event["eventId"].(string), RecordedAt: int64(event["recordedAt"].(float64)), PayloadDigest: digest}
	return contract.ValidateAuditReceipt(expected, receipt)
}

// Run accepts trusted SDK metadata, never a browser-supplied actor. Evaluate must be read-only.
func (d *Decision) Run(ctx context.Context, input map[string]any, evaluate func(context.Context) (bool, error)) (bool, error) {
	if evaluate == nil {
		return false, contract.ErrInvalidArgument
	}
	return d.run(ctx, input, func(ctx context.Context, _ string) (bool, error) { return evaluate(ctx) })
}

func (d *Decision) run(ctx context.Context, input map[string]any, evaluate func(context.Context, string) (bool, error)) (bool, error) {
	operation, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, e := snapshot(input)
	if e != nil {
		return false, contract.ErrInvalidArgument
	}
	if d.valid(request) != nil || request["productId"] != d.product || request["environment"] != d.environment || request["eventType"] != "heimdall.authorization.requested" || request["outcome"] != "requested" || evaluate == nil {
		return false, contract.ErrInvalidArgument
	}
	requestID := make([]byte, 32)
	if _, e = rand.Read(requestID); e != nil {
		return false, contract.ErrAuditUnavailable
	}
	request["eventId"] = hex.EncodeToString(requestID[:16])
	decisionID := hex.EncodeToString(requestID[16:])
	request["decisionId"] = decisionID
	if e = d.commit(request); e != nil {
		return false, e
	}
	value, failure := bounded(operation, func(ctx context.Context) (bool, error) { return evaluate(ctx, decisionID) })
	if failure == nil {
		failure = contract.CheckCancellation(operation)
	}
	outcome := "deny"
	if value {
		outcome = "allow"
	}
	if failure != nil {
		var known *contract.Error
		if !errors.As(failure, &known) {
			failure = contract.ErrUnavailable
		} else {
			failure = contract.NewError(known.Code, "")
		}
		switch failure.(*contract.Error).Code {
		case contract.Cancelled:
			outcome = "cancelled"
		case contract.InvalidArgument:
			outcome = "invalid"
		case contract.Unauthenticated, contract.Forbidden:
			outcome = "deny"
		default:
			outcome = "unavailable"
		}
	}
	completed, e := snapshot(request)
	if e != nil {
		return false, contract.ErrAuditUnavailable
	}
	id := make([]byte, 16)
	if _, e = rand.Read(id); e != nil {
		return false, contract.ErrAuditUnavailable
	}
	completed["eventId"] = hex.EncodeToString(id)
	completed["eventType"] = "heimdall.authorization.completed"
	completed["outcome"] = outcome
	now := max(request["recordedAt"].(float64), float64(d.now().UnixMilli()))
	completed["occurredAt"] = now
	completed["recordedAt"] = now
	if failure != nil {
		var known *contract.Error
		errors.As(failure, &known)
		completed["reasonCode"] = string(known.Code)
	}
	if e = d.commit(completed); e != nil {
		return false, e
	}
	if failure != nil {
		return false, failure
	}
	if e = contract.CheckCancellation(operation); e != nil {
		return false, e
	}
	return value, nil
}
