import type { Pool } from "pg";
import { validateAuditShape } from "../audit-shape.js";
import { validateAuditEvent } from "../audit.js";
import { checkCancellation, HeimdallError } from "../errors.js";
import { PostgresAudit } from "./audit.js";

export type CustomAuditValidator = Parameters<typeof validateAuditEvent>[2];

export function auditEventValidator(validateCustom?: CustomAuditValidator, customOnly = false) {
  return (event: Record<string, unknown>) => {
    validateAuditShape(event);
    validateAuditEvent(
      event as Parameters<typeof validateAuditEvent>[0],
      customOnly || event.source === "product" ? "product" : "sdk",
      validateCustom ?? (() => false),
    );
  };
}

export type CustomAuditEvent = Readonly<{
  eventId: string;
  eventType: string;
  eventVersion: number;
  occurredAt: number;
  recordedAt: number;
  tenantId: string | null;
  actor: { kind: "user"; issuer: string; subject: string } | { kind: "system"; id: string };
  target: { type: string; id: string };
  action: string;
  outcome: "succeeded" | "failed";
  requestId: string;
  attributes: Record<string, unknown>;
}>;

/** For trusted product code after resolving actor, tenant and enabled schema. */
export function createCustomAuditWriter(
  pool: Pool,
  options: {
    productId: string;
    environment: string;
    auditSinks: readonly string[];
    validateCustom: CustomAuditValidator;
  },
) {
  const { productId, environment, auditSinks, validateCustom } = options;
  if (typeof validateCustom !== "function") throw new HeimdallError("invalid_argument");
  const audit = PostgresAudit.forRetainedRuntime(
    pool,
    productId,
    environment,
    auditSinks,
    auditEventValidator(validateCustom, true),
  );
  return Object.freeze({
    async record(event: CustomAuditEvent, signal: AbortSignal) {
      if (
        !(signal instanceof AbortSignal) ||
        !event ||
        typeof event !== "object" ||
        (event.outcome !== "succeeded" && event.outcome !== "failed") ||
        (event.actor?.kind !== "user" && event.actor?.kind !== "system")
      )
        throw new HeimdallError("invalid_argument");
      checkCancellation(signal);
      const {
        eventId,
        eventType,
        eventVersion,
        occurredAt,
        recordedAt,
        tenantId,
        actor,
        target,
        action,
        outcome,
        requestId,
        attributes,
      } = event;
      return audit.append(
        {
          schemaVersion: 1,
          productId,
          environment,
          source: "product",
          sdkVersion: "0.0.0",
          eventId,
          eventType,
          eventVersion,
          occurredAt,
          recordedAt,
          tenantId,
          actor,
          target,
          action,
          outcome,
          requestId,
          attributes,
        },
        signal,
      );
    },
  });
}
