package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"time"
)

type CandidateObservation struct {
	Identity   contract.Identity
	Member     bool
	ObservedAt int64
}
type CandidateRepairMetadata struct {
	EventID, RequestID, PolicyVersion string
	OccurredAt, RecordedAt            int64
}

// RepairCandidates requires trusted observations obtained after reading expectedRevision.
func (o *OperationRecords) RepairCandidates(ctx context.Context, id, expectedRevision string, observations []CandidateObservation, m CandidateRepairMetadata) (*ManagedWorkspace, error) {
	_, revisionOK := workspaceDecimal(&expectedRevision, 9223372036854775807, true)
	if ctx == nil || !valid(id, 128) || !revisionOK || len(observations) < 1 || len(observations) > 100 || m.OccurredAt < 0 || m.RecordedAt < m.OccurredAt || m.RecordedAt > maxSafe {
		return nil, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	type observation struct {
		Hash      string `json:"hash"`
		Principal string `json:"principal"`
		Member    bool   `json:"member"`
		Observed  int64  `json:"observed"`
	}
	batch := make([]observation, 0, len(observations))
	seen := make(map[string]bool)
	for _, item := range observations {
		if item.ObservedAt < 0 || item.ObservedAt > m.RecordedAt {
			return nil, contract.ErrInvalidArgument
		}
		hash, e := identityBinding(item.Identity.Issuer, item.Identity.Subject)
		if e != nil {
			return nil, e
		}
		key := hex.EncodeToString(hash)
		if seen[key] {
			return nil, contract.ErrInvalidArgument
		}
		seen[key] = true
		principal, _ := contract.EncodeIdentity(item.Identity.Issuer, item.Identity.Subject)
		batch = append(batch, observation{key, principal, item.Member, item.ObservedAt})
	}
	oldest := m.RecordedAt
	for _, item := range batch {
		if item.Observed < oldest {
			oldest = item.Observed
		}
	}
	b := o.binding
	event := map[string]any{"schemaVersion": float64(1), "eventId": m.EventID, "eventType": "heimdall.recovery.completed", "eventVersion": float64(1), "occurredAt": float64(m.OccurredAt), "recordedAt": float64(m.RecordedAt), "productId": b.ProductID, "environment": b.Environment, "tenantId": id, "actor": map[string]any{"kind": "system", "id": "candidate-rebuild"}, "target": map[string]any{"type": "workspace", "id": id}, "action": "candidate.repair", "outcome": "succeeded", "requestId": m.RequestID, "source": "sdk", "sdkVersion": "0.0.0", "attributes": map[string]any{}, "modelId": b.ModelID, "manifestVersion": float64(b.ManifestVersion), "policyVersion": m.PolicyVersion}
	if e := validateOperationAudit(event); e != nil {
		return nil, e
	}
	var result *ManagedWorkspace
	_, e := o.audit.appendWith(ctx, event, func(ctx context.Context, tx *sql.Tx) error {
		var row workspaceRow
		err := tx.QueryRowContext(ctx, `UPDATE heimdall_managed.workspaces SET updated_at=$5
   WHERE product_id=$1 AND environment=$2 AND workspace_id=$3 AND revision=$4::bigint
   AND lifecycle='active' AND NOT mutation_blocked AND updated_at<=$5 AND updated_at<=$6
   RETURNING NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
   product_id,environment,workspace_id,name,lifecycle,mutation_blocked,revision::text,created_at::text,updated_at::text`, b.ProductID, b.Environment, id, expectedRevision, m.RecordedAt, oldest).Scan(&row.Primary, &row.Product, &row.Environment, &row.Workspace, &row.Name, &row.Lifecycle, &row.Blocked, &row.Revision, &row.Created, &row.Updated)
		if errors.Is(err, sql.ErrNoRows) {
			return contract.ErrConflict
		}
		if err != nil {
			return err
		}
		result, err = decodeWorkspace(row, contract.WorkspaceScope{ProductID: b.ProductID, Environment: b.Environment, WorkspaceID: id})
		if err != nil {
			return err
		}
		if result == nil {
			return contract.ErrUnavailable
		}
		payload, err := json.Marshal(batch)
		if err != nil {
			return contract.ErrUnavailable
		}
		var applied bool
		err = tx.QueryRowContext(ctx, `WITH batch AS MATERIALIZED (
  SELECT decode(x.hash,'hex') AS identity_hash,x.principal,x.member,x.observed
  FROM jsonb_to_recordset($4::jsonb) AS x(hash text,principal text,member boolean,observed bigint)
), incompatible AS MATERIALIZED (
  SELECT 1 FROM batch b JOIN heimdall_managed.membership_candidates c
    ON c.product_id=$1 AND c.environment=$2 AND c.workspace_id=$3 AND c.identity_hash=b.identity_hash
  WHERE c.principal<>b.principal OR c.observed_at>b.observed
), saved AS (
  INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at)
  SELECT $1,$2,$3,identity_hash,principal,observed FROM batch WHERE member AND NOT EXISTS (SELECT 1 FROM incompatible)
  ON CONFLICT(product_id,environment,identity_hash,workspace_id) DO UPDATE SET observed_at=EXCLUDED.observed_at
  WHERE membership_candidates.principal=EXCLUDED.principal AND membership_candidates.observed_at<=EXCLUDED.observed_at
  RETURNING 1
), removed AS (
  DELETE FROM heimdall_managed.membership_candidates c USING batch b
  WHERE c.product_id=$1 AND c.environment=$2 AND c.workspace_id=$3 AND c.identity_hash=b.identity_hash
  AND NOT b.member AND c.principal=b.principal AND c.observed_at<=b.observed
  AND NOT EXISTS (SELECT 1 FROM incompatible)
)
SELECT NOT EXISTS (SELECT 1 FROM incompatible) AND
  (SELECT count(*) FROM saved)=(SELECT count(*) FROM batch WHERE member) AS applied`, b.ProductID, b.Environment, id, string(payload)).Scan(&applied)
		if err == nil && !applied {
			return contract.ErrConflict
		}
		return err
	})
	if e != nil {
		return nil, e
	}
	return result, nil
}
