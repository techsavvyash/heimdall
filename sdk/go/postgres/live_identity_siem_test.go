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
	const product, environment = "support", "live-siem-go"
	if c.Stage == "collect" {
		collector, err := NewIdentityCollector(db, IdentityCollectorOptions{
			IdentityEventReaderOptions: provisioning.IdentityEventReaderOptions{
				Issuer: c.Issuer, OrganizationID: c.OrganizationID, Token: c.ProviderToken,
			},
			ProductID: product, Environment: environment, TenantID: "globex",
			AuditSinks: []string{"siem"}, StartMicros: c.StartMicros,
			Aggregates: []IdentityAggregate{{"user", c.Subject}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result, err := collector.Collect(ctx, IdentityEventCoverage{
			RetainedFromMicros: c.StartMicros - 1, VisibleBeforeMicros: c.StartMicros + 1,
			ValidUntilMs: time.Now().Add(25 * time.Second).UnixMilli(),
		}); err != nil || result != (IdentityCollection{NextMicros: c.StartMicros + 1, Recorded: 1}) {
			t.Fatal("live collection failed", result, err)
		}
	}
	delivery, err := NewAuditDelivery(db, AuditDeliveryOptions{
		ProductID: product, Environment: environment, SinkID: "siem", Endpoint: c.Endpoint,
		Token: c.SinkToken, TransportProfile: "loopback",
	})
	if err != nil {
		t.Fatal(err)
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
