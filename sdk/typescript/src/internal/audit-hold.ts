import { createHash } from "node:crypto";
import { validateAuditShape } from "./audit-shape.js";
import { validateAuditEvent } from "./audit.js";
import { HeimdallError } from "./errors.js";

export type AuditHoldRequest = {
  productId: string;
  environment: string;
  tenantId: string | null;
  holdId: string;
  eventId: string;
  purpose: "administrative" | "retry";
  operation: "acquire" | "release";
  at: number;
  policyVersion: string;
};
function auditHoldEventId(r: AuditHoldRequest): string {
  return (
    "hold-" +
    createHash("sha256")
      .update(
        `heimdall.audit-hold.v1\0${r.productId}\0${r.environment}\0${r.holdId}\0${r.operation}`,
      )
      .digest("hex")
  );
}
// Binding is not actor authentication, scope authorization or proof that a retry obligation ended.
export function validateAuditHold(r: AuditHoldRequest, event: Record<string, unknown>): void {
  const token = (v: unknown, max: number) =>
    typeof v === "string" && v.length <= max && /^[A-Za-z0-9_-]+(?![\s\S])/.test(v);
  if (
    !token(r.productId, 64) ||
    !token(r.environment, 64) ||
    (r.tenantId !== null && !token(r.tenantId, 128)) ||
    !token(r.holdId, 128) ||
    !token(r.eventId, 128) ||
    !token(r.policyVersion, 128) ||
    !["administrative", "retry"].includes(r.purpose) ||
    !["acquire", "release"].includes(r.operation) ||
    !Number.isSafeInteger(r.at) ||
    r.at < 0
  )
    throw new HeimdallError("invalid_argument");
  validateAuditShape(event);
  validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
  const actor = event.actor as { kind: string };
  const target = event.target as { type: string; id: string };
  if (
    event.eventId !== auditHoldEventId(r) ||
    event.eventId === r.eventId ||
    event.eventType !== "heimdall.policy.completed" ||
    event.source !== "sdk" ||
    event.outcome !== "succeeded" ||
    actor.kind === "unknown" ||
    event.productId !== r.productId ||
    event.environment !== r.environment ||
    event.tenantId !== r.tenantId ||
    event.operationId !== r.holdId ||
    event.policyVersion !== r.policyVersion ||
    event.action !== `audit.hold.${r.purpose}.${r.operation}` ||
    target.type !== "audit_event" ||
    target.id !== r.eventId ||
    event.recordedAt !== r.at ||
    event.occurredAt !== r.at ||
    Object.hasOwn(event, "decisionId")
  )
    throw new HeimdallError("invalid_argument");
}
