package contract

import (
	"bytes"
	"encoding/json"
	"io"
)

type RecoveryOptions struct {
	TerminalID, RequestID, SDKVersion string
	Actor                             map[string]any
	At, Cutoff                        int64
}
type RecoveryProposal struct {
	FormatVersion  int            `json:"formatVersion"`
	Cutoff         int64          `json:"cutoff"`
	RequestDigest  string         `json:"requestDigest"`
	Terminal       map[string]any `json:"terminal"`
	Administration map[string]any `json:"administration"`
}

// BuildRecoveryProposal uses explicit time/IDs so retries do not consult clocks.
func BuildRecoveryProposal(request map[string]any, options RecoveryOptions) (*RecoveryProposal, error) {
	digest, e := AuditDigest(request)
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(request)
	if e != nil {
		return nil, ErrInvalidArgument
	}
	var original map[string]any
	if json.Unmarshal(raw, &original) != nil {
		return nil, ErrInvalidArgument
	}
	product, _ := original["productId"].(string)
	environment, _ := original["environment"].(string)
	if _, e = BindAuditDecision(product, environment, []map[string]any{original}); e != nil {
		return nil, e
	}
	if options.Cutoff < 0 || options.Cutoff > 9007199254740991 || options.At < options.Cutoff || options.At > 9007199254740991 {
		return nil, ErrInvalidArgument
	}
	terminal := map[string]any{}
	for key, value := range original {
		terminal[key] = value
	}
	terminal["eventId"] = options.TerminalID
	terminal["eventType"] = "heimdall.authorization.completed"
	terminal["outcome"] = "outcome_unknown"
	terminal["occurredAt"] = float64(options.At)
	terminal["recordedAt"] = float64(options.At)
	terminal["sdkVersion"] = options.SDKVersion
	delete(terminal, "reasonCode")
	administration := map[string]any{
		"schemaVersion": float64(1), "eventVersion": float64(1), "eventId": RecoveryEventID(product, environment, options.TerminalID), "eventType": "heimdall.recovery.completed",
		"productId": product, "environment": environment, "actor": options.Actor, "target": map[string]any{"type": "authorization_decision", "id": original["decisionId"]}, "action": "authorization.recover",
		"outcome": "succeeded", "occurredAt": float64(options.At), "recordedAt": float64(options.At), "requestId": options.RequestID, "decisionId": original["decisionId"], "source": "sdk", "sdkVersion": options.SDKVersion, "attributes": map[string]any{},
	}
	if tenant, exists := original["tenantId"]; exists {
		administration["tenantId"] = tenant
	}
	p := &RecoveryProposal{1, options.Cutoff, digest, terminal, administration}
	if e = ValidateRecoveryProposal(original, p); e != nil {
		return nil, e
	}
	raw, e = json.Marshal(p)
	if e != nil {
		return nil, ErrInvalidArgument
	}
	var snapshot RecoveryProposal
	if json.Unmarshal(raw, &snapshot) != nil {
		return nil, ErrInvalidArgument
	}
	return &snapshot, nil
}

// ValidateRecoveryProposal binds persisted proposals to digest-verified history.
func ValidateRecoveryProposal(request map[string]any, p *RecoveryProposal) error {
	if p == nil || p.FormatVersion != 1 {
		return ErrInvalidArgument
	}
	digest, e := AuditDigest(request)
	if e != nil {
		return e
	}
	if p.RequestDigest != digest {
		return ErrInvalidArgument
	}
	for _, event := range []map[string]any{p.Terminal, p.Administration} {
		if e = ValidateAuditShape(event); e != nil {
			return e
		}
		raw, e := json.Marshal(event)
		if e != nil {
			return ErrInvalidArgument
		}
		var typed AuditEvent
		if json.Unmarshal(raw, &typed) != nil {
			return ErrInvalidArgument
		}
		if e = ValidateAuditEvent(typed, "sdk", func(string, int, map[string]any) bool { return false }); e != nil {
			return e
		}
		if _, e = AuditDigest(event); e != nil {
			return e
		}
	}
	product, _ := request["productId"].(string)
	environment, _ := request["environment"].(string)
	binding, e := BindAuditDecision(product, environment, []map[string]any{request, p.Terminal})
	if e != nil {
		return e
	}
	if e = ValidateRecoveryPair(p.Terminal, p.Administration, p.Cutoff); e != nil {
		return e
	}
	if binding.RequestedAt > p.Cutoff {
		return ErrInvalidArgument
	}
	return nil
}

// DecodeRecoveryProposal rejects unknown wire fields and trailing JSON values.
func DecodeRecoveryProposal(request map[string]any, raw []byte) (*RecoveryProposal, error) {
	if len(raw) > 4500000 {
		return nil, ErrInvalidArgument
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var wire map[string]any
	if decoder.Decode(&wire) != nil || len(wire) != 5 {
		return nil, ErrInvalidArgument
	}
	for _, key := range []string{"formatVersion", "cutoff", "requestDigest", "terminal", "administration"} {
		if _, ok := wire[key]; !ok {
			return nil, ErrInvalidArgument
		}
	}
	version, vOK := wire["formatVersion"].(float64)
	cutoff, cOK := wire["cutoff"].(float64)
	digest, dOK := wire["requestDigest"].(string)
	terminal, tOK := wire["terminal"].(map[string]any)
	admin, aOK := wire["administration"].(map[string]any)
	if !vOK || version != 1 || !cOK || cutoff < 0 || cutoff > 9007199254740991 || cutoff != float64(int64(cutoff)) || !dOK || !tOK || !aOK {
		return nil, ErrInvalidArgument
	}
	p := RecoveryProposal{1, int64(cutoff), digest, terminal, admin}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, ErrInvalidArgument
	}
	if e := ValidateRecoveryProposal(request, &p); e != nil {
		return nil, e
	}
	return &p, nil
}

// RecoveryProposalDigest binds retry intent, not caller authorization.
func RecoveryProposalDigest(request map[string]any, p *RecoveryProposal) (string, error) {
	if e := ValidateRecoveryProposal(request, p); e != nil {
		return "", e
	}
	terminal, e := AuditDigest(p.Terminal)
	if e != nil {
		return "", e
	}
	administration, e := AuditDigest(p.Administration)
	if e != nil {
		return "", e
	}
	return AuditDigest(map[string]any{"kind": "recovery_proposal", "formatVersion": float64(1), "cutoff": float64(p.Cutoff), "requestDigest": p.RequestDigest, "terminalDigest": terminal, "administrationDigest": administration})
}
