package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestRetainedDelivery(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETAINED_DELIVERY_URL")
	if url == "" {
		t.Skip("retained delivery database required")
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	port, err := NewRetainedRuntimeAudit(db, "ledger", "delivery-go", []string{"siem"}, backfillPort(t, db, "ledger").validate)
	if err != nil {
		t.Fatal(err)
	}
	batch := contract.AuditDelivery{ProductID: "ledger", Environment: "delivery-go", SinkID: "siem", EventIDs: []string{"ordinary"}}
	if os.Getenv("HEIMDALL_TEST_RETAINED_DELIVERY_REJECT") == "1" {
		_, readErr := port.Pending(ctx, "siem", 2000, 8)
		_, claimErr := port.BeginAttempt(ctx, "siem", "ordinary", 0, 2000, 3000)
		_, deferErr := port.DeferAttempt(ctx, "siem", "ordinary", 1, 4000)
		_, quarantineErr := port.Quarantine(ctx, "siem", "ordinary", 0)
		ackErr := port.Acknowledge(ctx, batch, batch, 5000)
		for _, err := range []error{readErr, claimErr, deferErr, quarantineErr, ackErr} {
			if !errors.Is(err, contract.ErrUnavailable) {
				t.Fatal("expected readiness refusal", err)
			}
		}
		return
	}
	event := capacityWritesData(t).Ordinary
	event["environment"] = "delivery-go"
	if _, err := port.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	pending, err := port.Pending(ctx, "siem", 2000, 8)
	if err != nil || len(pending) != 1 || pending[0].EventID != "ordinary" || pending[0].Event == nil {
		t.Fatal("pending delivery", err)
	}
	if attempt, err := port.BeginAttempt(ctx, "siem", "ordinary", 0, 2000, 3000); err != nil || attempt == nil || *attempt != 1 {
		t.Fatal("claim", err)
	}
	if ok, err := port.DeferAttempt(ctx, "siem", "ordinary", 1, 4000); err != nil || !ok {
		t.Fatal("defer", err)
	}
	if err := port.Acknowledge(ctx, batch, batch, 5000); err != nil {
		t.Fatal(err)
	}
	if pending, err := port.Pending(ctx, "siem", 6000, 8); err != nil || len(pending) != 0 {
		t.Fatal("acknowledged delivery still pending", err)
	}
}
