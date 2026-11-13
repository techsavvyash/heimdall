package auditflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
)

func testManagedInventory(t *testing.T, bearer string, now int64, issuer, audience string) {
	var cases []struct {
		Name, Expected string
		NextIDs        []*string
		Reads          int
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/managed-inventory.json")
	if err != nil || json.Unmarshal(raw, &cases) != nil {
		t.Fatal("invalid inventory fixture", err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			reads := 0
			outcome := ""
			auth, err := NewAuthentication(
				identity.StatusConfig{Config: identity.Config{Issuer: issuer, Audience: audience, JWKSURI: issuer + "/keys", Profile: "production"}, ProductID: "ledger", Environment: "production", PolicyVersion: "p1", ClientID: "client"},
				func(context.Context) (string, error) { return "secret", nil },
				identity.Gate{Check: func(context.Context, identity.Identity) (bool, error) { return false, nil }, Invalidate: func(context.Context, identity.Negative) error { return nil }},
				func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
					if event["eventType"] == "heimdall.export.completed" {
						outcome = event["outcome"].(string)
					}
					return receipt(event), nil
				}, func() time.Time { return time.UnixMilli(now) })
			if err != nil {
				t.Fatal(err)
			}
			page, err := auth.RunManagedInventory(context.Background(), bearer, ManagedExportRequest{Kind: "workspaces", Limit: 1, OperationID: "inventory"}, "model", 1,
				func(context.Context, MaintenanceContext) (bool, error) { return true, nil },
				func(ctx context.Context, consume func(context.Context, contract.ManagedExportScan) error) error {
					return consume(ctx, func(_ context.Context, kind string, workspace, _ *string, _ int) ([]byte, error) {
						var next any = fmt.Sprintf("page-%03d", reads)
						if len(c.NextIDs) > 0 {
							next = c.NextIDs[reads]
						}
						reads++
						return json.Marshal(map[string]any{"schemaVersion": 1, "productId": "ledger", "environment": "production", "kind": kind, "workspaceId": workspace,
							"items": []map[string]string{{"workspaceId": fmt.Sprintf("row-%d", reads)}}, "nextId": next})
					})
				})
			status, wantOutcome := "inventory", "succeeded"
			if err != nil {
				status, wantOutcome = err.Error(), "failed"
			}
			if status != c.Expected || reads != c.Reads || outcome != wantOutcome {
				t.Fatal(status, reads, outcome)
			}
			if status == "inventory" {
				var result struct{ Items []json.RawMessage }
				if json.Unmarshal(page, &result) != nil || len(result.Items) != 2 {
					t.Fatal("incomplete inventory")
				}
			} else if page != nil {
				t.Fatal("partial inventory released")
			}
		})
	}
}
