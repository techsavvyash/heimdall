package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

func TestLiveIdentitySIEM(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_IDENTITY_SIEM")
	if raw == "" {
		t.Skip("requires live identity and Elasticsearch verification")
	}
	var c struct {
		Stage, Issuer, OrganizationID, ProviderToken, Subject, Endpoint, SinkToken string
		StartMicros                                                                int64
		Policy                                                                     IdentityCoveragePolicy
		ExpectedEvents                                                             int
		AggregateType                                                              string
	}
	if json.Unmarshal([]byte(raw), &c) != nil {
		t.Fatal("invalid verification configuration")
	}
	db, err := sql.Open("postgres", os.Getenv("HEIMDALL_TEST_AUDIT_DELIVERY_DB"))
	if err != nil {
		t.Fatal("invalid verification database")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	const product = "support"
	environment, tenant := "live-siem-go", "globex"
	if c.Stage == "product" {
		environment, tenant = "verification", ""
	}
	if c.Stage == "collect" || c.Stage == "scheduled" {
		collector, err := NewIdentityCollector(db, IdentityCollectorOptions{
			IdentityEventReaderOptions: provisioning.IdentityEventReaderOptions{
				Issuer: c.Issuer, OrganizationID: c.OrganizationID, Token: c.ProviderToken,
			},
			ProductID: product, Environment: environment, TenantID: "globex",
			AuditSinks: []string{"siem"}, StartMicros: c.StartMicros,
			Aggregates: []IdentityAggregate{{c.AggregateType, c.Subject}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if c.Stage == "scheduled" {
			before, err := collector.Checkpoint(ctx)
			if err != nil || before == nil {
				t.Fatal("scheduled checkpoint unavailable", err)
			}
			coverage, err := DeriveIdentityEventCoverage(c.Policy, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			result, err := collector.Collect(ctx, coverage)
			if err != nil || result.NextMicros <= before.NextMicros || result.Recorded != c.ExpectedEvents {
				t.Fatal("scheduled checkpoint did not advance with the expected event count", result, err)
			}
			return
		}
		if result, err := collector.Collect(ctx, IdentityEventCoverage{
			RetainedFromMicros: c.StartMicros - 1, VisibleBeforeMicros: c.StartMicros + 1,
			ValidUntilMs: time.Now().Add(25 * time.Second).UnixMilli(),
		}); err != nil || result != (IdentityCollection{NextMicros: c.StartMicros + 1, Recorded: 1}) {
			t.Fatal("live collection failed", result, err)
		}
	}
	delivery, err := NewAuditDelivery(db, AuditDeliveryOptions{
		ProductID: product, Environment: environment, SinkID: "siem", TenantID: tenant, Endpoint: c.Endpoint,
		Token: c.SinkToken, TransportProfile: "loopback",
		ValidateCustom: func(kind string, version int, attributes map[string]any) bool {
			count, ok := attributes["count"].(float64)
			return kind == "product.ticket.listed" && version == 1 && len(attributes) == 1 &&
				ok && count >= 0 && count <= 100 && count == float64(int(count))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Stage == "product" {
		for range 50 {
			result, err := delivery.Deliver(ctx, 20)
			if err != nil {
				t.Fatal("product audit delivery failed", err)
			}
			if result.Attempted == 0 {
				return
			}
		}
		t.Fatal("product export did not drain within the page budget")
	}
	result, err := delivery.Deliver(ctx, 10)
	if c.Stage == "retry" {
		if err != nil || result.Delivered != 1 {
			t.Fatal("live delivery failed", result, err)
		}
	} else if !errors.Is(err, contract.ErrUnavailable) {
		t.Fatal("expected loss of sink response", result, err)
	}
}
