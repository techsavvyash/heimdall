import { ManagedExport } from "./internal/postgres/export.js";
import { RecordMaintenance } from "./internal/postgres/maintenance.js";
import { OperationRecords } from "./internal/postgres/operations.js";
import { WorkspaceRecords } from "./internal/postgres/workspaces.js";
import { CandidateRecords } from "./internal/postgres/candidates.js";
import { workspaceGate } from "./internal/workspace-contract.js";
import { HeimdallError } from "./internal/errors.js";
import { readSession } from "./internal/browser-session.js";
import { RecordCipher } from "./internal/envelopes.js";
import { BrowserLoginRecords } from "./internal/postgres/login.js";
import { PostgresRecords } from "./internal/postgres/records.js";
import { SessionRecords } from "./internal/postgres/sessions.js";
import type { Pool } from "pg";
import type { RuntimeStorage } from "./runtime.js";
import { IdentityRecords } from "./internal/postgres/identity.js";
import { PostgresAudit } from "./internal/postgres/audit.js";
import {
  checkKeyUsageReadiness,
  checkRetentionReadiness,
  checkManagedRetentionReadiness,
} from "./internal/postgres/readiness.js";
import { validateAuditShape } from "./internal/audit-shape.js";
import { validateAuditEvent } from "./internal/audit.js";

export { createDirectoryDeactivation } from "./internal/postgres/directory-deactivations.js";
export { createDirectoryCleanup } from "./internal/postgres/directory-cleanup.js";
export { createDirectoryPolling } from "./internal/postgres/directory-polling.js";
export { createDirectoryEnrollment } from "./internal/postgres/directory-enrollment.js";
export { createAuditDelivery } from "./internal/postgres/audit-delivery.js";
export { createCustomAuditWriter } from "./internal/postgres/custom-audit.js";
export type { CustomAuditEvent, CustomAuditValidator } from "./internal/postgres/custom-audit.js";
export {
  createDirectoryReactivation,
  createDirectoryReactivationRelease,
} from "./internal/postgres/directory-reactivation.js";

export type PostgresStorageOptions = {
  productId: string;
  environment: string;
  /** Explicit initial routing. An empty list stores events without delivery rows. */
  auditSinks: readonly string[];
  sessions?: {
    keyUsagePool: Pool;
    activeKeyId: string;
    keys: Readonly<Record<string, Uint8Array>>;
  };
};

/** Compose durable runtime ports. The caller owns the pool and runs migrations separately. */
export function createPostgresStorage(pool: Pool, options: PostgresStorageOptions): RuntimeStorage {
  const productId = options?.productId;
  const environment = options?.environment;
  const identities = IdentityRecords.forRetainedRuntime(pool, productId, environment);
  const audit = PostgresAudit.forRetainedRuntime(
    pool,
    productId,
    environment,
    options.auditSinks,
    (event) => {
      validateAuditShape(event);
      validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
    },
  );
  const sessionOptions = options.sessions;
  const keyPool = sessionOptions?.keyUsagePool;
  if (sessionOptions && !keyPool) throw new HeimdallError("invalid_argument");
  const cipher =
    sessionOptions && new RecordCipher(sessionOptions.activeKeyId, sessionOptions.keys);
  const browser =
    keyPool &&
    cipher &&
    Object.freeze({
      login: new BrowserLoginRecords(
        new PostgresRecords(pool, keyPool, cipher, productId, environment),
      ),
      sessions: new SessionRecords(pool, keyPool, cipher, productId, environment, readSession),
    });
  return Object.freeze({
    productId,
    environment,
    ready: async (signal: AbortSignal) => {
      await checkRetentionReadiness(pool, signal);
      if (keyPool) await checkKeyUsageReadiness(keyPool, signal);
    },
    ...(browser ? { browser } : {}),
    identity: Object.freeze(identities),
    appendAudit: audit.append,
  });
}

/** Managed runtime storage always gates tenant checks and owns discovery candidates. */
export function createManagedPostgresStorage(
  pool: Pool,
  options: PostgresStorageOptions & { exportPool?: Pool },
): RuntimeStorage {
  const storage = createPostgresStorage(pool, options);
  const { productId, environment } = storage;
  const workspaces = WorkspaceRecords.forRetainedRuntime(pool, productId, environment);
  const candidates = CandidateRecords.forRetainedRuntime(pool, productId, environment);
  const auditSinks = [...options.auditSinks];
  const managed: NonNullable<RuntimeStorage["managed"]> = {
    workspace: workspaces.get.bind(workspaces),
    ...(options.exportPool
      ? {
          exportReader: ManagedExport.forRetainedRuntime(
            options.exportPool,
            productId,
            environment,
          ),
        }
      : {}),
    operations(binding) {
      if (binding.productId !== productId || binding.environment !== environment)
        throw new HeimdallError("invalid_argument");
      return OperationRecords.forRetainedRuntime(pool, binding, auditSinks);
    },
    async allows(workspaceId, signal) {
      return (
        workspaceGate(await workspaces.get(workspaceId, signal), {
          productId,
          environment,
          workspaceId,
        }) === "check_relationships"
      );
    },
    source(actor) {
      return {
        async scan({ after, limit }, { signal }) {
          const page = await candidates.scan(actor, after ?? "0", limit, signal);
          return {
            items: page.items.map(({ position, workspace }) => ({
              position,
              workspace: { id: workspace.workspaceId, name: workspace.name },
            })),
            exhausted: page.nextPosition === null,
          };
        },
      };
    },
  };
  return Object.freeze({
    ...storage,
    async ready(signal: AbortSignal) {
      await storage.ready(signal);
      await checkManagedRetentionReadiness(pool, signal);
    },
    managed: Object.freeze(managed),
  });
}

/** Bounded cleanup for expired session and login records with the caller's pool. */
export function createRecordMaintenance(pool: Pool, productId: string, environment: string) {
  if (typeof pool?.connect !== "function") throw new HeimdallError("invalid_argument");
  const maintenance = new RecordMaintenance(pool, productId, environment);
  return Object.freeze({
    cleanupExpired: maintenance.cleanupExpired.bind(maintenance),
    cleanupLag: maintenance.cleanupLag.bind(maintenance),
  });
}

export {
  createIdentityCollector,
  type IdentityCollectorOptions,
  type IdentityCollectorCheckpoint,
} from "./internal/postgres/identity-collector.js";
