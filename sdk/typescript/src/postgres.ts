import type { Pool } from "pg";
import type { RuntimeStorage } from "./runtime.js";
import { IdentityRecords } from "./internal/postgres/identity.js";
import { PostgresAudit } from "./internal/postgres/audit.js";
import { checkAuditReadiness } from "./internal/postgres/readiness.js";
import { validateAuditShape } from "./internal/audit-shape.js";
import { validateAuditEvent } from "./internal/audit.js";

export type PostgresStorageOptions = {
  productId: string;
  environment: string;
  /** Explicit initial routing. An empty list stores events without delivery rows. */
  auditSinks: readonly string[];
};

/** Compose durable runtime ports. The caller owns the pool and runs migrations separately. */
export function createPostgresStorage(pool: Pool, options: PostgresStorageOptions): RuntimeStorage {
  const productId = options?.productId;
  const environment = options?.environment;
  const identities = new IdentityRecords(pool, productId, environment);
  const audit = new PostgresAudit(pool, productId, environment, options.auditSinks, (event) => {
    validateAuditShape(event);
    validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
  });
  return Object.freeze({
    productId,
    environment,
    ready: (signal: AbortSignal) => checkAuditReadiness(pool, signal),
    identity: Object.freeze(identities.identityGate(productId, environment)),
    appendAudit: audit.appendRuntime.bind(audit),
  });
}
