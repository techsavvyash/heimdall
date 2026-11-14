import { randomUUID } from "node:crypto";
import { createCustomAuditWriter } from "@heimdall/sdk/postgres";

export function createInvoiceReadAudit(pool, config, auditSinks) {
  const eventType = "product.invoice.listed";
  const writer = createCustomAuditWriter(pool, {
    ...config,
    auditSinks,
    validateCustom: (type, version, attributes) =>
      type === eventType &&
      version === 1 &&
      Object.keys(attributes).length === 1 &&
      Number.isInteger(attributes.count) &&
      attributes.count >= 0 &&
      attributes.count <= 100,
  });
  return async (actor, tenantId, count, signal) => {
    const now = Date.now();
    await writer.record(
      {
        eventId: randomUUID(),
        eventType,
        eventVersion: 1,
        occurredAt: now,
        recordedAt: now,
        tenantId,
        actor: { kind: "user", issuer: actor.issuer, subject: actor.subject },
        target: { type: "workspace", id: tenantId },
        action: "invoice.list",
        outcome: "succeeded",
        requestId: randomUUID(),
        attributes: { count },
      },
      signal,
    );
  };
}
