import { HeimdallError } from "./errors.js";

export const auditTypes: Readonly<Record<string, readonly string[]>> = {
  "heimdall.authorization.requested": ["requested"],
  "heimdall.authorization.completed": [
    "allow",
    "deny",
    "invalid",
    "unavailable",
    "cancelled",
    "outcome_unknown",
  ],
  "heimdall.membership.intent": ["requested"],
  "heimdall.membership.attempt": ["attempted"],
  "heimdall.membership.completed": ["succeeded", "failed", "outcome_unknown"],
  "heimdall.ownership.intent": ["requested"],
  "heimdall.ownership.attempt": ["attempted"],
  "heimdall.ownership.completed": ["succeeded", "failed", "outcome_unknown"],
  "heimdall.authentication.completed": ["succeeded", "deny", "invalid", "unavailable", "cancelled"],
  "heimdall.recovery.completed": ["succeeded", "failed", "outcome_unknown"],
  "heimdall.policy.completed": ["succeeded", "failed"],
  "heimdall.export.completed": ["succeeded", "failed"],
  "heimdall.identity.observed": ["succeeded", "failed"],
};
type AuditEvent = {
  eventType: string;
  eventVersion: number;
  occurredAt: number;
  recordedAt: number;
  actor: { kind: "user" | "unknown" | "system" };
  tenantId: string | null;
  source: string;
  outcome: string;
  attributes: Record<string, unknown>;
  decisionId?: string;
  operationId?: string;
  permission?: string;
  modelId?: string;
  manifestVersion?: number;
  policyVersion?: string;
};
const forbidden = new Set([
  "token",
  "accesstoken",
  "refreshtoken",
  "idtoken",
  "password",
  "secret",
  "clientsecret",
  "authorization",
  "cookie",
  "setcookie",
  "ciphertext",
  "actor",
  "productid",
  "environment",
  "tenantid",
  "outcome",
  "eventid",
  "__proto__",
  "constructor",
  "prototype",
]);
function attributesWeight(value: unknown, depth = 0): number {
  if (depth > 8) throw new HeimdallError("invalid_argument");
  if (value === null || typeof value === "boolean") return 1;
  if (typeof value === "number") {
    if (!Number.isFinite(value) || Math.abs(value) > Number.MAX_SAFE_INTEGER)
      throw new HeimdallError("invalid_argument");
    return 9;
  }
  if (typeof value === "string") {
    if (!value.isWellFormed() || Buffer.byteLength(value, "utf8") > 4096)
      throw new HeimdallError("invalid_argument");
    return 1 + Buffer.byteLength(value, "utf8");
  }
  if (Array.isArray(value)) {
    if (value.length > 32) throw new HeimdallError("invalid_argument");
    let size = 1;
    for (const item of value) {
      size += attributesWeight(item, depth + 1);
      if (size > 32768) throw new HeimdallError("invalid_argument");
    }
    return size;
  }
  if (typeof value !== "object" || value === undefined) throw new HeimdallError("invalid_argument");
  const entries = Object.entries(value);
  if (entries.length > 32) throw new HeimdallError("invalid_argument");
  let size = 1;
  for (const [key, v] of entries) {
    if (
      !key ||
      key.length > 64 ||
      /[^A-Za-z0-9_-]/.test(key) ||
      forbidden.has(key) ||
      forbidden.has(key.replace(/[_-]/g, "").toLowerCase())
    )
      throw new HeimdallError("invalid_argument");
    size += key.length + attributesWeight(v, depth + 1);
    if (size > 32768) throw new HeimdallError("invalid_argument");
  }
  return size;
}

// Origin is a trusted internal call path, never a field supplied by a client.
// This validates event semantics; it does not verify an actor or persist anything.
export function validateAuditEvent(
  event: AuditEvent,
  origin: "sdk" | "product",
  custom: (type: string, version: number, attributes: Record<string, unknown>) => boolean,
): void {
  const invalid = () => {
    throw new HeimdallError("invalid_argument");
  };
  if (origin !== "sdk" && origin !== "product") invalid();
  if (event.occurredAt > event.recordedAt || attributesWeight(event.attributes) > 32768) invalid();
  if (origin === "product") {
    if (
      !event.eventType.startsWith("product.") ||
      event.source !== "product" ||
      !custom(event.eventType, event.eventVersion, event.attributes)
    )
      invalid();
    return;
  }
  if (
    !Object.hasOwn(auditTypes, event.eventType) ||
    event.eventVersion !== 1 ||
    !auditTypes[event.eventType]!.includes(event.outcome) ||
    event.source === "product" ||
    Object.keys(event.attributes).length !== 0
  )
    invalid();
  if (
    (event.outcome === "allow" ||
      (event.eventType === "heimdall.authentication.completed" && event.outcome === "succeeded")) &&
    event.actor.kind !== "user"
  )
    invalid();
  if (
    event.eventType.startsWith("heimdall.authorization.") &&
    (!event.decisionId ||
      !event.modelId ||
      !event.manifestVersion ||
      (["allow", "deny"].includes(event.outcome) && (!event.tenantId || !event.permission)))
  )
    invalid();
  if (
    (event.eventType.startsWith("heimdall.membership.") ||
      event.eventType.startsWith("heimdall.ownership.")) &&
    (!event.operationId ||
      !event.tenantId ||
      !event.modelId ||
      !event.manifestVersion ||
      event.actor.kind === "unknown")
  )
    invalid();
  if (event.eventType === "heimdall.policy.completed" && !event.policyVersion) invalid();
}
