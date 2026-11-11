package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"regexp"
	"time"
)

type OperationBinding struct {
	ProductID, Environment, StoreID, ModelID, ModelHash string
	ManifestVersion                                     int
}
type OperationAllocation struct {
	WorkspaceID, OperationID, EventID, RequestID, PolicyVersion string
	OccurredAt, RecordedAt                                      int64
}
type ManagedOperation struct {
	SchemaVersion                                                  int
	OperationID, WorkspaceID                                       string
	Intent                                                         contract.Intent
	Fingerprint, KeyDigest, State                                  string
	DispatchCount, Generation, NextAttemptAt, CreatedAt, UpdatedAt int64
}
type operationRow struct {
	ProductID       string `json:"productId"`
	Environment     string `json:"environment"`
	OperationID     string `json:"operationId"`
	WorkspaceID     string `json:"workspaceId"`
	Kind            string `json:"kind"`
	ActorHash       string `json:"actorHash"`
	TargetHash      string `json:"targetHash"`
	KeyDigest       string `json:"keyDigest"`
	Fingerprint     string `json:"fingerprint"`
	Payload         string `json:"payload"`
	StoreID         string `json:"storeId"`
	ModelID         string `json:"modelId"`
	ModelHash       string `json:"modelHash"`
	ManifestVersion int    `json:"manifestVersion"`
	State           string `json:"state"`
	DispatchCount   string `json:"dispatchCount"`
	Generation      string `json:"generation"`
	NextAttemptAt   string `json:"nextAttemptAt"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

var operationHex = regexp.MustCompile(`^[a-f0-9]{64}$`)

func decodeOperation(r operationRow, b OperationBinding, actorHash string) (*ManagedOperation, error) {
	if !valid(r.OperationID, 128) || !operationHex.MatchString(r.KeyDigest) || r.ActorHash != actorHash || r.ProductID != b.ProductID || r.Environment != b.Environment || r.StoreID != b.StoreID || r.ModelID != b.ModelID || r.ModelHash != b.ModelHash || r.ManifestVersion != b.ManifestVersion {
		return nil, contract.ErrUnavailable
	}
	i, e := contract.ParseBoundIntent([]byte(r.Payload), contract.IntentBinding{ProductID: b.ProductID, Environment: b.Environment, WorkspaceID: r.WorkspaceID, Kind: r.Kind, ActorHash: r.ActorHash, TargetHash: r.TargetHash, Fingerprint: r.Fingerprint, ManifestVersion: b.ManifestVersion})
	if e != nil {
		return nil, contract.ErrUnavailable
	}
	count, c := workspaceDecimal(&r.DispatchCount, maxSafe, false)
	generation, g := workspaceDecimal(&r.Generation, maxSafe, true)
	due, d := workspaceDecimal(&r.NextAttemptAt, maxSafe, false)
	created, a := workspaceDecimal(&r.CreatedAt, maxSafe, false)
	updated, u := workspaceDecimal(&r.UpdatedAt, maxSafe, false)
	if !c || !g || !d || !a || !u || created > updated {
		return nil, contract.ErrUnavailable
	}
	switch r.State {
	case "pending", "retryable":
	case "dispatching", "confirmed", "uncertain", "completed":
		if count < 1 {
			return nil, contract.ErrUnavailable
		}
	case "cancelled":
		if count != 0 {
			return nil, contract.ErrUnavailable
		}
	default:
		return nil, contract.ErrUnavailable
	}
	return &ManagedOperation{1, r.OperationID, r.WorkspaceID, i, r.Fingerprint, r.KeyDigest, r.State, count, generation, due, created, updated}, nil
}

const operationColumns = `jsonb_build_object('productId',o.product_id,'environment',o.environment,'operationId',o.operation_id,'workspaceId',o.workspace_id,'kind',o.kind,
 'actorHash',encode(o.actor_hash,'hex'),'targetHash',encode(o.target_hash,'hex'),'keyDigest',encode(o.key_digest,'hex'),'fingerprint',encode(o.fingerprint,'hex'),
 'payload',convert_from(o.intent_payload,'UTF8'),'storeId',o.store_id,'modelId',o.model_id,'modelHash',encode(o.model_hash,'hex'),'manifestVersion',o.manifest_version,
 'state',o.state,'dispatchCount',o.dispatch_count::text,'generation',o.generation::text,'nextAttemptAt',o.next_attempt_at::text,'createdAt',o.created_at::text,'updatedAt',o.updated_at::text)`

func operationSelect(byKey bool) string {
	where := "o.operation_id=$4"
	if byKey {
		where = "o.key_digest=$4"
	}
	return "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary, CASE WHEN o.operation_id IS NULL THEN NULL ELSE " + operationColumns + " END AS record FROM (SELECT 1) seed LEFT JOIN heimdall_managed.operations o ON o.product_id=$1 AND o.environment=$2 AND o.actor_hash=$3 AND " + where
}
func validateOperationAudit(event map[string]any) error {
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
}

type OperationRecords struct {
	db      *sql.DB
	binding OperationBinding
	audit   *Audit
}

// Internal storage only: callers must authenticate and authorize before creating intent.
func NewOperationRecords(db *sql.DB, b OperationBinding, sinks []string) (*OperationRecords, error) {
	if db == nil || !valid(b.ProductID, 64) || !valid(b.Environment, 64) || !valid(b.StoreID, 128) || !valid(b.ModelID, 128) || !operationHex.MatchString(b.ModelHash) || b.ManifestVersion < 1 || b.ManifestVersion > 2147483647 {
		return nil, contract.ErrInvalidArgument
	}
	audit, e := NewAudit(db, b.ProductID, b.Environment, sinks, validateOperationAudit)
	if e != nil {
		return nil, e
	}
	return &OperationRecords{db, b, audit}, nil
}

type operationReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (o *OperationRecords) read(ctx context.Context, db operationReader, selector string, actorHash []byte, byKey bool) (*ManagedOperation, error) {
	if ctx == nil {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	var argument any = selector
	if byKey {
		argument, _ = hex.DecodeString(selector)
	}
	var primary bool
	var raw []byte
	e := db.QueryRowContext(ctx, operationSelect(byKey), o.binding.ProductID, o.binding.Environment, actorHash, argument).Scan(&primary, &raw)
	if e != nil {
		return nil, databaseError(ctx)
	}
	if e = contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if !primary {
		return nil, contract.ErrUnavailable
	}
	if raw == nil {
		return nil, nil
	}
	var row operationRow
	if json.Unmarshal(raw, &row) != nil {
		return nil, contract.ErrUnavailable
	}
	r, e := decodeOperation(row, o.binding, hex.EncodeToString(actorHash))
	if e != nil {
		return nil, e
	}
	got := r.OperationID
	if byKey {
		got = r.KeyDigest
	}
	if got != selector {
		return nil, contract.ErrUnavailable
	}
	return r, nil
}
func (o *OperationRecords) Get(ctx context.Context, id string, actor contract.Identity) (*ManagedOperation, error) {
	if ctx == nil || !valid(id, 128) {
		return nil, contract.ErrInvalidArgument
	}
	hash, e := identityBinding(actor.Issuer, actor.Subject)
	if e != nil {
		return nil, e
	}
	return o.read(ctx, o.db, id, hash, false)
}
func (o *OperationRecords) Create(ctx context.Context, intentJSON []byte, key string, a OperationAllocation) (*ManagedOperation, error) {
	if ctx == nil {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	i, e := contract.ParseIntent(intentJSON)
	if e != nil {
		return nil, e
	}
	d, e := contract.IntentDigests(i, key)
	if e != nil {
		return nil, e
	}
	b := o.binding
	if i.Kind != "create_workspace" || i.ProductID != b.ProductID || i.Environment != b.Environment || i.ManifestVersion != b.ManifestVersion || !valid(a.WorkspaceID, 128) || !valid(a.OperationID, 128) || a.OccurredAt < 0 || a.RecordedAt < a.OccurredAt || a.RecordedAt > maxSafe {
		return nil, contract.ErrInvalidArgument
	}
	event := map[string]any{"schemaVersion": float64(1), "eventId": a.EventID, "eventType": "heimdall.membership.intent", "eventVersion": float64(1), "occurredAt": float64(a.OccurredAt), "recordedAt": float64(a.RecordedAt), "productId": b.ProductID, "environment": b.Environment, "tenantId": a.WorkspaceID, "actor": map[string]any{"kind": "user", "issuer": i.Actor.Issuer, "subject": i.Actor.Subject}, "target": map[string]any{"type": "workspace", "id": a.WorkspaceID}, "action": "workspace.create", "outcome": "requested", "requestId": a.RequestID, "source": "sdk", "sdkVersion": "0.0.0", "attributes": map[string]any{}, "operationId": a.OperationID, "modelId": b.ModelID, "manifestVersion": float64(b.ManifestVersion), "policyVersion": a.PolicyVersion}
	if e = validateOperationAudit(event); e != nil {
		return nil, e
	}
	actorHash, _ := identityBinding(i.Actor.Issuer, i.Actor.Subject)
	targetHash, _ := identityBinding(i.Target.Issuer, i.Target.Subject)
	payload, _ := json.Marshal(i)
	existing, e := o.read(ctx, o.db, d.KeyDigest, actorHash, true)
	if e != nil {
		return nil, e
	}
	if existing != nil {
		if existing.Fingerprint != d.Fingerprint {
			return nil, contract.ErrConflict
		}
		return existing, nil
	}
	var result *ManagedOperation
	_, e = o.audit.appendWith(ctx, event, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES($1,$2,$3,$4,'pending',true,$5,$5)", b.ProductID, b.Environment, a.WorkspaceID, i.Name, a.RecordedAt); err != nil {
			return err
		}
		keyDigest, _ := hex.DecodeString(d.KeyDigest)
		fingerprint, _ := hex.DecodeString(d.Fingerprint)
		modelHash, _ := hex.DecodeString(b.ModelHash)
		if _, err := tx.ExecContext(ctx, `INSERT INTO heimdall_managed.operations(product_id,environment,operation_id,workspace_id,kind,actor_hash,target_hash,key_digest,fingerprint,intent_payload,store_id,model_id,model_hash,manifest_version,state,dispatch_count,generation,next_attempt_at,created_at,updated_at)
 VALUES($1,$2,$3,$4,'create_workspace',$5,$6,$7,$8,$9,$10,$11,$12,$13,'pending',0,1,$14,$14,$14)`, b.ProductID, b.Environment, a.OperationID, a.WorkspaceID, actorHash, targetHash, keyDigest, fingerprint, payload, b.StoreID, b.ModelID, modelHash, b.ManifestVersion, a.RecordedAt); err != nil {
			return err
		}
		var err error
		result, err = o.read(ctx, tx, a.OperationID, actorHash, false)
		if err != nil {
			return err
		}
		if result == nil {
			return contract.ErrUnavailable
		}
		return nil
	})
	if errors.Is(e, contract.ErrOutcomeUnknown) {
		return nil, contract.NewError(contract.OutcomeUnknown, a.OperationID)
	}
	if errors.Is(e, contract.ErrConflict) {
		won, err := o.read(ctx, o.db, d.KeyDigest, actorHash, true)
		if err != nil {
			return nil, err
		}
		if won != nil && won.Fingerprint == d.Fingerprint {
			return won, nil
		}
	}
	if e != nil {
		return nil, e
	}
	return result, nil
}
