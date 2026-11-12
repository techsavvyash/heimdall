import { createHash } from "node:crypto";
import { HeimdallError } from "./errors.js";

export type AuditRetirementRequest = {
  productId: string;
  environment: string;
  tenantId: string | null;
  retirementId: string;
  eventId: string;
  at: number;
  retainForMs: number;
  policyVersion: string;
};

// Called after full audit-event validation. This binds the request, actor and policy.
export function validateAuditRetirement(
  r: AuditRetirementRequest,
  event: Record<string, unknown>,
): void {
  const token = (v: unknown, max: number) =>
    typeof v === "string" && v.length <= max && /^[A-Za-z0-9_-]+(?![\s\S])/.test(v);
  if (
    !token(r.productId, 64) ||
    !token(r.environment, 64) ||
    (r.tenantId !== null && !token(r.tenantId, 128)) ||
    !token(r.retirementId, 128) ||
    !token(r.eventId, 128) ||
    !token(r.policyVersion, 128) ||
    !Number.isSafeInteger(r.at) ||
    r.at < 0 ||
    !Number.isSafeInteger(r.retainForMs) ||
    r.retainForMs < 1
  )
    throw new HeimdallError("invalid_argument");
  const id =
    "retirement-" +
    createHash("sha256")
      .update(
        `heimdall.audit-retirement.v1\0${r.productId}\0${r.environment}\0${r.retirementId}\0${r.retainForMs}`,
      )
      .digest("hex");
  const actor = event.actor as { kind: string };
  const target = event.target as { type: string; id: string };
  if (
    event.eventId !== id ||
    id === r.eventId ||
    event.eventType !== "heimdall.policy.completed" ||
    event.source !== "sdk" ||
    event.outcome !== "succeeded" ||
    (actor.kind !== "system" && actor.kind !== "user") ||
    event.productId !== r.productId ||
    event.environment !== r.environment ||
    event.tenantId !== r.tenantId ||
    event.operationId !== r.retirementId ||
    event.policyVersion !== r.policyVersion ||
    event.action !== "audit.retention.retire" ||
    target.type !== "audit_event" ||
    target.id !== r.eventId ||
    event.recordedAt !== r.at ||
    event.occurredAt !== r.at ||
    Object.hasOwn(event, "decisionId")
  )
    throw new HeimdallError("invalid_argument");
}
