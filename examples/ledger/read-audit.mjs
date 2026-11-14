import { randomUUID } from "node:crypto";
import { createCustomAuditWriter } from "@heimdall/sdk/postgres";

const eventType = "product.invoice.listed";
export function validateInvoiceRead(type, version, attributes) {
  return (
    type === eventType &&
    version === 1 &&
    Object.keys(attributes).length === 1 &&
    Number.isInteger(attributes.count) &&
    attributes.count >= 0 &&
    attributes.count <= 100
  );
}

export function createInvoiceReadAudit(pool, config, auditSinks) {
  const writer = createCustomAuditWriter(pool, {
    ...config,
    auditSinks,
    validateCustom: validateInvoiceRead,
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
