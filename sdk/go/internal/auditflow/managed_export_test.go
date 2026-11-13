package auditflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func testManagedExport(t *testing.T, bearer string, now int64, issuer, audience string) {
	var cases []struct {
		Name, Kind, Fault, Expected string
		WorkspaceID                 *string
		Scans                       int
	}
	raw, e := os.ReadFile("../../../../contracts/fixtures/audited-managed-export.json")
	if e != nil || json.Unmarshal(raw, &cases) != nil {
		t.Fatal("invalid audited export fixture", e)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var revoked atomic.Bool
			scans := 0
			lastOutcome := ""
			completion := ""
			auth, err := NewAuthentication(
				identity.StatusConfig{Config: identity.Config{Issuer: issuer, Audience: audience, JWKSURI: issuer + "/keys", Profile: "production"}, ProductID: "ledger", Environment: "production", PolicyVersion: "p1", ClientID: "client"},
				func(context.Context) (string, error) { return "secret", nil },
				identity.Gate{Check: func(context.Context, identity.Identity) (bool, error) { return revoked.Load(), nil }, Invalidate: func(context.Context, identity.Negative) error { return nil }},
				func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
					lastOutcome = event["outcome"].(string)
					if event["eventType"] == "heimdall.export.completed" {
						completion = lastOutcome
					}
					r := receipt(event)
					if (c.Fault == "receipt" && completion != "") || (c.Fault == "decision" && lastOutcome == "allow") {
						r.EventID = "wrong"
					}
					return r, nil
				},
				func() time.Time { return time.UnixMilli(now) })
			if err != nil {
				t.Fatal(err)
			}
			page, err := auth.RunManagedExport(context.Background(), bearer, ManagedExportRequest{
				Kind: c.Kind, WorkspaceID: c.WorkspaceID, Limit: 1, OperationID: "export-1",
			}, "model", 1, func(_ context.Context, scope MaintenanceContext) (bool, error) {
				return tenantValue(scope.TenantID) == tenantValue(c.WorkspaceID) && scope.Action == "managed.export."+c.Kind && c.Fault != "deny", nil
			}, func(_ context.Context, kind string, workspace, _ *string, _ int) ([]byte, error) {
				scans++
				if lastOutcome != "allow" {
					return nil, contract.ErrUnavailable
				}
				revoked.Store(c.Fault == "revoke")
				product := "ledger"
				if c.Fault == "scope" {
					product = "support"
				}
				return json.Marshal(map[string]any{"schemaVersion": 1, "productId": product, "environment": "production", "kind": kind, "workspaceId": workspace, "items": []any{}, "nextId": nil})
			})
			status := "page"
			if err != nil {
				status = err.Error()
			}
			wantCompletion := ""
			if c.Scans > 0 {
				wantCompletion = "succeeded"
				if c.Fault == "scope" {
					wantCompletion = "failed"
				}
			}
			if status != c.Expected || scans != c.Scans || completion != wantCompletion || (page != nil) != (status == "page") {
				t.Fatalf("status=%s scans=%d completion=%s", status, scans, completion)
			}
		})
	}
}

func testLiveManagedExport(t *testing.T, bearer string, now int64, issuer, audience string) {
	readerURL := os.Getenv("HEIMDALL_TEST_AUDITED_EXPORT_READER")
	if readerURL == "" {
		t.Skip("retained managed database required")
	}
	reader, err := sql.Open("postgres", readerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_AUDITED_EXPORT_WRITER"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	store, err := postgres.NewRetainedManagedExport(reader, "ledger", "production")
	if err != nil {
		t.Fatal(err)
	}
	audit, err := postgres.NewRetainedRuntimeAudit(writer, "ledger", "production", nil, contract.ValidateAuditShape)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuthentication(
		identity.StatusConfig{Config: identity.Config{Issuer: issuer, Audience: audience, JWKSURI: issuer + "/keys", Profile: "production"}, ProductID: "ledger", Environment: "production", PolicyVersion: "p1", ClientID: "client"},
		func(context.Context) (string, error) { return "secret", nil },
		identity.Gate{Check: func(context.Context, identity.Identity) (bool, error) { return false, nil }, Invalidate: func(context.Context, identity.Negative) error { return nil }},
		audit.Append, func() time.Time { return time.UnixMilli(now) })
	if err != nil {
		t.Fatal(err)
	}
	workspace := "export-probe"
	page, err := auth.RunManagedExport(context.Background(), bearer, ManagedExportRequest{Kind: "workspaces", WorkspaceID: &workspace, Limit: 1, OperationID: "export-go"}, "model", 1,
		func(_ context.Context, scope MaintenanceContext) (bool, error) {
			return tenantValue(scope.TenantID) == workspace, nil
		}, store.Scan)
	var result struct{ Items []map[string]any }
	want := map[string]any{"productId": "ledger", "environment": "production", "workspaceId": workspace, "lifecycle": "suspended", "mutationBlocked": true, "revision": "", "createdAt": float64(1), "updatedAt": float64(2)}
	if err != nil || json.Unmarshal(page, &result) != nil || len(result.Items) != 1 {
		t.Fatal("unexpected sanitized page", err)
	}
	revision, ok := result.Items[0]["revision"].(string)
	want["revision"] = revision
	if !ok || revision == "" || !reflect.DeepEqual(result.Items[0], want) {
		t.Fatal("unexpected sanitized fields")
	}

}
