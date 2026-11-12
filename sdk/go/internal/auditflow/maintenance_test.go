package auditflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func TestAuditedMaintenance(t *testing.T) {
	read := func(name string, dest any) {
		b, e := os.ReadFile("../../../../contracts/fixtures/" + name + ".json")
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, dest); e != nil {
			t.Fatal(e)
		}
	}
	var token struct {
		Issuer, Audience, Subject string
		Now                       int64
		JWKS                      json.RawMessage
		Vectors                   []struct{ Name, Token string }
	}
	var provider []struct {
		Expected            string
		Introspection, User json.RawMessage
	}
	var cases []struct {
		Name, Fault, Expected   string
		TenantID, GrantedTenant *string
		Outcomes                []string
		PolicyCalls             int
	}
	read("token-verification", &token)
	read("identity-status", &provider)
	read("audited-maintenance", &cases)
	var raw string
	for _, v := range token.Vectors {
		if v.Name == "valid" {
			raw = v.Token
		}
	}
	var active int
	for i, c := range provider {
		if c.Expected == "active" {
			active = i
			break
		}
	}
	previous := http.DefaultTransport
	defer func() { http.DefaultTransport = previous }()
	http.DefaultTransport = authTransport(func(r *http.Request) (*http.Response, error) {
		body := provider[active].User
		if strings.HasSuffix(r.URL.Path, "/keys") {
			body = token.JWKS
		}
		if strings.HasSuffix(r.URL.Path, "/introspect") {
			body = provider[active].Introspection
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})
	var durable *postgres.RetainedRuntimeAudit
	if url := os.Getenv("HEIMDALL_TEST_MAINTENANCE_AUDIT_URL"); url != "" {
		db, e := sql.Open("postgres", url)
		if e != nil {
			t.Fatal(e)
		}
		defer db.Close()
		durable, e = postgres.NewRetainedRuntimeAudit(db, "ledger", "production", []string{"siem"}, contract.ValidateAuditShape)
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var denied atomic.Bool
			var now atomic.Int64
			now.Store(token.Now)
			events := []map[string]any{}
			a, e := NewAuthentication(
				identity.StatusConfig{Config: identity.Config{Issuer: token.Issuer, Audience: token.Audience, JWKSURI: token.Issuer + "/keys", Profile: "production"}, ProductID: "ledger", Environment: "production", PolicyVersion: "p1", ClientID: "client"},
				func(context.Context) (string, error) { return "secret", nil },
				identity.Gate{Check: func(context.Context, identity.Identity) (bool, error) { return denied.Load(), nil }, Invalidate: func(context.Context, identity.Negative) error { return nil }},
				func(ctx context.Context, event map[string]any) (contract.AuditReceipt, error) {
					events = append(events, event)
					r := receipt(event)
					if c.Fault == "request" && event["outcome"] == "requested" {
						return r, contract.ErrUnavailable
					}
					if durable != nil {
						result, e := durable.Append(ctx, event)
						if e != nil {
							return r, e
						}
						r = result
					}
					if c.Fault == "terminal" && event["outcome"] == "allow" {
						r.EventID = "mismatch"
					}
					return r, nil
				},
				func() time.Time { return time.UnixMilli(now.Load()) },
			)
			if e != nil {
				t.Fatal(e)
			}
			action := "audit.retention.retire"
			bearer := raw
			if c.Fault == "action" {
				action = "invoice.approve"
			}
			if c.Fault == "token" {
				bearer = "invalid"
			}
			policyCalls, applyCalls := 0, 0
			request := MaintenanceRequest{TenantID: c.TenantID, Action: action, TargetEventID: "target", OperationID: "retire-1", ModelID: "model", ManifestVersion: 1}
			_, e = a.RunMaintenance(context.Background(), bearer, request,
				func(_ context.Context, scope MaintenanceContext) (bool, error) {
					policyCalls++
					if c.Fault == "policy" {
						return false, contract.ErrUnavailable
					}
					if c.Fault == "revoked" {
						denied.Store(true)
					}
					if c.Fault == "stale" {
						now.Add(60000)
					}
					allowed := reflect.DeepEqual(scope.TenantID, c.GrantedTenant)
					if scope.TenantID != nil {
						*scope.TenantID = "mutated"
					}
					return allowed, nil
				},
				func(_ context.Context, scope MaintenanceContext) (contract.AuditReceipt, error) {
					applyCalls++
					if !reflect.DeepEqual(scope.MaintenanceRequest, request) || scope.ProductID != "ledger" || scope.Environment != "production" ||
						scope.PolicyVersion != "p1" || scope.Actor.Issuer != token.Issuer || scope.Actor.Subject != token.Subject ||
						len(events) != 3 || events[2]["outcome"] != "allow" {
						return contract.AuditReceipt{}, contract.ErrInvalidArgument
					}
					return contract.AuditReceipt{}, nil
				},
			)
			status := "receipt"
			if e != nil {
				status = e.Error()
			}
			wantApply := 0
			if c.Expected == "receipt" {
				wantApply = 1
			}
			if status != c.Expected || policyCalls != c.PolicyCalls || applyCalls != wantApply {
				t.Fatalf("status=%s policy=%d apply=%d", status, policyCalls, applyCalls)
			}
			outcomes := []string{}
			for _, event := range events {
				outcomes = append(outcomes, event["outcome"].(string))
			}
			if !reflect.DeepEqual(outcomes, c.Outcomes) {
				t.Fatal(outcomes, c.Outcomes)
			}
			if len(events) >= 2 {
				d := events[1]
				if d["productId"] != "ledger" || d["environment"] != "production" || d["operationId"] != "retire-1" ||
					d["permission"] != action || d["action"] != action || !reflect.DeepEqual(d["tenantId"], tenantValue(c.TenantID)) {
					t.Fatal("decision lost scope")
				}
			}
		})
	}
	t.Run("bound maintenance command", func(t *testing.T) {
		testMaintenanceCommand(t, raw, token.Now, token.Issuer, token.Audience, token.Subject)
	})

}
func tenantValue(t *string) any {
	if t == nil {
		return nil
	}
	return *t
}
