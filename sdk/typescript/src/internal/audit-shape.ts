import { errorCodes, HeimdallError } from "./errors.js";
const token = /^[A-Za-z0-9_-]+(?![\s\S])/;
const required = [
  "schemaVersion",
  "eventId",
  "eventType",
  "eventVersion",
  "occurredAt",
  "recordedAt",
  "productId",
  "environment",
  "tenantId",
  "actor",
  "target",
  "action",
  "outcome",
  "requestId",
  "source",
  "sdkVersion",
  "attributes",
];
const optional = [
  "decisionId",
  "operationId",
  "parentDecisionId",
  "permission",
  "modelId",
  "manifestVersion",
  "policyVersion",
  "reasonCode",
];
const invalid = (): never => {
  throw new HeimdallError("invalid_argument");
};
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return invalid();
  return value as Record<string, unknown>;
}
function fields(value: unknown, keys: string[], extra: string[] = []): Record<string, unknown> {
  const v = object(value);
  if (
    keys.some((k) => !Object.hasOwn(v, k)) ||
    Object.keys(v).some((k) => !keys.includes(k) && !extra.includes(k))
  )
    invalid();
  return v;
}
function string(value: unknown, max: number, pattern?: RegExp): void {
  if (
    typeof value !== "string" ||
    !value.isWellFormed() ||
    Array.from(value).length < 1 ||
    Array.from(value).length > max ||
    (pattern && !pattern.test(value))
  )
    invalid();
}
function integer(value: unknown, min: number, max: number): void {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < min || value > max)
    invalid();
}
// Structural validation is bundled; semantic origin/actor checks remain mandatory.
export function validateAuditShape(value: unknown): void {
  const e = fields(value, required, optional);
  if (e.schemaVersion !== 1) invalid();
  string(e.eventId, 128, token);
  string(e.eventType, 128, /^(heimdall|product)([.][a-z][a-z0-9_]*)+(?![\s\S])/);
  integer(e.eventVersion, 1, 2147483647);
  integer(e.occurredAt, 0, Number.MAX_SAFE_INTEGER);
  integer(e.recordedAt, 0, Number.MAX_SAFE_INTEGER);
  string(e.productId, 64, token);
  string(e.environment, 64, token);
  if (e.tenantId !== null) string(e.tenantId, 128, token);
  const actor = object(e.actor);
  if (actor.kind === "user") {
    fields(actor, ["kind", "issuer", "subject"]);
    string(actor.issuer, 2048);
    string(actor.subject, 1024);
  } else if (actor.kind === "system") {
    fields(actor, ["kind", "id"]);
    string(actor.id, 128, token);
  } else if (actor.kind === "unknown") fields(actor, ["kind"]);
  else invalid();
  const target = fields(e.target, ["type", "id"]);
  string(target.type, 64, token);
  string(target.id, 8192);
  string(e.action, 128);
  string(e.requestId, 128, token);
  string(e.sdkVersion, 64);
  if (
    ![
      "requested",
      "attempted",
      "allow",
      "deny",
      "invalid",
      "unavailable",
      "cancelled",
      "outcome_unknown",
      "succeeded",
      "failed",
    ].includes(e.outcome as string)
  )
    invalid();
  if (!["sdk", "product", "operator", "zitadel"].includes(e.source as string)) invalid();
  if (Object.keys(object(e.attributes)).length > 32) invalid();
  for (const key of ["decisionId", "operationId", "parentDecisionId", "modelId", "policyVersion"])
    if (Object.hasOwn(e, key)) string(e[key], 128, token);
  if (Object.hasOwn(e, "permission")) string(e.permission, 128);
  if (Object.hasOwn(e, "manifestVersion")) integer(e.manifestVersion, 1, 2147483647);
  if (
    Object.hasOwn(e, "reasonCode") &&
    !errorCodes.includes(e.reasonCode as (typeof errorCodes)[number])
  )
    invalid();
}
