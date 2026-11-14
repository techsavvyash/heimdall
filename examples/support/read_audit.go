package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/postgres"
)

const ticketReadEventType = "product.ticket.listed"

func validateTicketRead(kind string, version int, attributes map[string]any) bool {
	count, ok := attributes["count"].(float64)
	return kind == ticketReadEventType && version == 1 && len(attributes) == 1 &&
		ok && count >= 0 && count <= 100 && count == float64(int(count))
}

func ticketReadAudit(db *sql.DB, product, environment string, sinks []string) (func(context.Context, *heimdall.Principal, string, int) error, error) {
	writer, err := postgres.NewCustomAuditWriter(db, postgres.CustomAuditOptions{
		ProductID: product, Environment: environment, AuditSinks: sinks,
		ValidateCustom: validateTicketRead,
	})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, actor *heimdall.Principal, tenant string, count int) error {
		now := time.Now().UnixMilli()
		id := actor.Identity()
		var correlation [32]byte
		if _, err := rand.Read(correlation[:]); err != nil {
			return err
		}
		_, err := writer.Record(ctx, postgres.CustomAuditEvent{
			EventID: hex.EncodeToString(correlation[:16]), RequestID: hex.EncodeToString(correlation[16:]),
			EventType: ticketReadEventType, EventVersion: 1, OccurredAt: now, RecordedAt: now,
			TenantID: &tenant, Actor: map[string]any{"kind": "user", "issuer": id.Issuer, "subject": id.Subject},
			Target: map[string]any{"type": "workspace", "id": tenant}, Action: "ticket.list",
			Outcome: "succeeded", Attributes: map[string]any{"count": float64(count)},
		})
		return err
	}, nil
}
