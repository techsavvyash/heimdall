package provisioning

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

// Driven by the isolated OpenFGA stop/drain probe. Credentials travel via environment.
func TestPinnedWorkspaceRecoveryProbe(t *testing.T) {
	raw := os.Getenv("HEIMDALL_RECOVERY_PROBE_INPUT")
	if raw == "" {
		t.Skip("isolated pinned recovery probe")
	}
	var input struct {
		Config, Manifest               json.RawMessage
		Phase, OperationID, EvidenceID string
		Generation                     int64
	}
	if json.Unmarshal([]byte(raw), &input) != nil {
		t.Fatal("invalid recovery probe input")
	}
	d, err := configuration.Parse(input.Config, input.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	cfg := d.Config()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid recovery probe database")
	}
	defer db.Close()
	binding := postgres.OperationBinding{
		ProductID: cfg.ProductID, Environment: cfg.Environment, StoreID: cfg.OpenFGA.StoreID,
		ModelID: cfg.OpenFGA.ModelID, ModelHash: d.Artifact().ModelHash, ManifestVersion: cfg.ManifestVersion,
	}
	records, err := postgres.NewRetainedOperationRecords(db, binding, []string{"siem"})
	if err != nil {
		t.Fatal(err)
	}
	actor := contract.Identity{Issuer: cfg.Issuer, Subject: "recovery-probe"}
	opID := input.OperationID
	switch input.Phase {
	case "dispatch":
		intent := contract.Intent{SchemaVersion: 1, ProductID: cfg.ProductID, Environment: cfg.Environment,
			Kind: "create_workspace", Actor: actor, Target: actor, Name: opID, Roles: []string{},
			ManifestVersion: cfg.ManifestVersion}
		payload, err := json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().UnixMilli()
		op, err := records.Create(ctx, payload, opID, postgres.OperationAllocation{
			WorkspaceID: opID, OperationID: opID, EventID: opID + "-intent", RequestID: opID,
			PolicyVersion: "probe", OccurredAt: at, RecordedAt: at,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = records.Advance(ctx, opID, actor, op.Generation, "dispatch", postgres.OperationTransitionMetadata{
			EventID: opID + "-dispatch", RequestID: opID, PolicyVersion: "probe",
			OccurredAt: at, RecordedAt: at, NextAttemptAt: at,
		})
		if err != nil {
			t.Fatal(err)
		}
	case "recover":
		_, err := RecoverWorkspaceCreation(ctx, db, input.Config, input.Manifest, WorkspaceRecoveryOptions{
			OperationID: opID, ExpectedGeneration: input.Generation, InitiatingIssuer: actor.Issuer,
			InitiatingSubject: actor.Subject, PolicyVersion: "probe", AuditSinks: []string{"siem"},
			RecoveryToken: os.Getenv("HEIMDALL_PROBE_OPENFGA_KEY"),
			VerifyQuiescence: func(_ context.Context, scope WorkspaceRecoveryScope) (WorkspaceRecoveryEvidence, error) {
				if scope.OperationID != opID || scope.WorkspaceID != opID ||
					scope.Generation != input.Generation || scope.StoreID != cfg.OpenFGA.StoreID ||
					scope.ModelID != cfg.OpenFGA.ModelID || scope.Environment != cfg.Environment {
					return WorkspaceRecoveryEvidence{}, contract.ErrForbidden
				}
				return WorkspaceRecoveryEvidence{OperatorID: "probe", EvidenceID: input.EvidenceID}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	case "observe":
	default:
		t.Fatal("unknown recovery probe phase")
	}
	op, err := records.Get(ctx, opID, actor)
	if err != nil || op == nil {
		t.Fatal("journal read", err)
	}
	workspaces, err := postgres.NewRetainedWorkspaceRecords(db, cfg.ProductID, cfg.Environment)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaces.Get(ctx, op.WorkspaceID)
	if err != nil || workspace == nil {
		t.Fatal("workspace read", err)
	}
	candidates, err := postgres.NewRetainedCandidateRecords(db, cfg.ProductID, cfg.Environment)
	if err != nil {
		t.Fatal(err)
	}
	page, err := candidates.Scan(ctx, actor, "0", 100)
	if err != nil {
		t.Fatal(err)
	}
	indexed := false
	for _, item := range page.Items {
		indexed = indexed || item.Workspace.WorkspaceID == op.WorkspaceID
	}
	var count int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM heimdall.audit_events e
		JOIN heimdall.audit_deliveries d USING (product_id, environment, event_id)
		WHERE e.product_id=$1 AND e.environment=$2 AND d.sink_id='siem'
		AND convert_from(decode(translate(e.event->>'payload','-_','+/') ||
		repeat('=', (4-length(e.event->>'payload')%4)%4), 'base64'),'UTF8')::jsonb
		@> jsonb_build_object('operationId',$3::text,'requestId',$4::text,'actor',jsonb_build_object('kind','system','id','probe'))`,
		cfg.ProductID, cfg.Environment, opID, input.EvidenceID).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(map[string]any{
		"state": op.State, "generation": op.Generation, "blocked": workspace.MutationBlocked,
		"lifecycle": workspace.Lifecycle, "indexed": indexed, "audited": count,
	})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("RECOVERY_PROBE %s\n", result)
}
