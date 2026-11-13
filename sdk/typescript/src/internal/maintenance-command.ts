import { createHash } from "node:crypto";
import type { RetirementCursor } from "./postgres/audit.js";
import { auditHoldEventId, type AuditHoldRequest } from "./audit-hold.js";
import { auditRetirementEventId, type AuditRetirementRequest } from "./audit-retire.js";
import type { AuditedAuthentication } from "./audited-authentication.js";
import { HeimdallError, checkCancellation } from "./errors.js";

type RunMaintenance = AuditedAuthentication["runMaintenance"];
type Store = {
  applyHold: (
    request: AuditHoldRequest,
    event: Record<string, unknown>,
    signal?: AbortSignal,
  ) => ReturnType<RunMaintenance>;
  retire: (
    request: AuditRetirementRequest,
    event: Record<string, unknown>,
    signal?: AbortSignal,
  ) => ReturnType<RunMaintenance>;
};
type Operation = Pick<
  Parameters<RunMaintenance>[1],
  "tenantId" | "action" | "targetEventId" | "operationId"
> & {
  at: number;
};

type RetirementSweep = {
  id: string;
  tenantId: string | null;
  at: number;
  after: RetirementCursor | null;
};
type RetirementBatch = {
  productId: string;
  environment: string;
  policyVersion: string;
  modelId: string;
  manifestVersion: number;
  retainForMs: number;
  sweep: RetirementSweep;
  operations: Operation[];
  next: RetirementCursor | null;
};
const token = (v: unknown) =>
  typeof v === "string" && v.length > 0 && v.length <= 128 && !/[^A-Za-z0-9_-]/.test(v);

function retirementOperationId(
  batch: Omit<RetirementBatch, "operations" | "next">,
  target: string,
) {
  const fields = [
    "heimdall.retirement-proposal.v1",
    batch.productId,
    batch.environment,
    batch.policyVersion,
    batch.modelId,
    batch.manifestVersion,
    batch.sweep.tenantId ?? "",
    batch.sweep.id,
    batch.sweep.at,
    batch.retainForMs,
    target,
  ];
  return "sweep-" + createHash("sha256").update(fields.join("\0")).digest("hex");
}

const holdActions = {
  "audit.hold.administrative.acquire": ["administrative", "acquire"],
  "audit.hold.administrative.release": ["administrative", "release"],
  "audit.hold.retry.acquire": ["retry", "acquire"],
  "audit.hold.retry.release": ["retry", "release"],
} as const;

// The operator supplies a stable proposal. Deployment policy supplies the model,
// retention duration, clock and the concrete maintenance port.
export class MaintenanceCommand {
  private readonly store: Store;

  constructor(
    private readonly authentication: AuditedAuthentication,
    store: Store,
    private readonly modelId: string,
    private readonly manifestVersion: number,
    private readonly retainForMs: number,
    private readonly authorize: Parameters<RunMaintenance>[2],
    private readonly now: () => number = Date.now,
  ) {
    if (
      !authentication ||
      typeof store?.retire !== "function" ||
      typeof store?.applyHold !== "function" ||
      typeof modelId !== "string" ||
      !/^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(modelId) ||
      !Number.isInteger(manifestVersion) ||
      manifestVersion < 1 ||
      manifestVersion > 2147483647 ||
      !Number.isSafeInteger(retainForMs) ||
      retainForMs < 1 ||
      typeof authorize !== "function" ||
      typeof now !== "function"
    )
      throw new HeimdallError("invalid_argument");
    this.store = { applyHold: store.applyHold.bind(store), retire: store.retire.bind(store) };
  }

  async queueRetirementPage(
    input: RetirementSweep,
    limit: number,
    scan: (
      tenant: string | null,
      now: number,
      duration: number,
      after: RetirementCursor | null,
      limit: number,
      signal?: AbortSignal,
    ) => Promise<{ eventIds: string[]; next: RetirementCursor | null }>,
    save: (batch: RetirementBatch, signal?: AbortSignal) => Promise<boolean>,
    signal?: AbortSignal,
  ): Promise<void> {
    const now = this.now();
    if (
      !input ||
      !token(input.id) ||
      (input.tenantId !== null && !token(input.tenantId)) ||
      !Number.isSafeInteger(now) ||
      !Number.isSafeInteger(input.at) ||
      input.at < 0 ||
      input.at > now ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 8 ||
      typeof scan !== "function" ||
      typeof save !== "function"
    )
      throw new HeimdallError("invalid_argument");
    const sweep = {
      id: input.id,
      tenantId: input.tenantId,
      at: input.at,
      after: input.after && { ...input.after },
    };
    const scope = this.authentication.maintenanceScope();
    const matches = (cursor: RetirementCursor) =>
      cursor &&
      cursor.productId === scope.productId &&
      cursor.environment === scope.environment &&
      cursor.tenantId === sweep.tenantId &&
      cursor.now === sweep.at &&
      cursor.retainForMs === this.retainForMs &&
      token(cursor.eventId);
    if (sweep.after !== null && !matches(sweep.after)) throw new HeimdallError("invalid_argument");
    if (signal) checkCancellation(signal);
    const page = await scan(
      sweep.tenantId,
      sweep.at,
      this.retainForMs,
      sweep.after && { ...sweep.after },
      limit,
      signal,
    );
    if (
      !page ||
      !Array.isArray(page.eventIds) ||
      page.eventIds.length > limit ||
      !page.eventIds.every(token) ||
      new Set(page.eventIds).size !== page.eventIds.length ||
      (page.next !== null && (!matches(page.next) || page.next.eventId === sweep.after?.eventId))
    )
      throw new HeimdallError("audit_unavailable");
    const binding = {
      ...scope,
      modelId: this.modelId,
      manifestVersion: this.manifestVersion,
      retainForMs: this.retainForMs,
      sweep,
    };
    const operations: Operation[] = page.eventIds.map((targetEventId) => ({
      tenantId: sweep.tenantId,
      action: "audit.retention.retire",
      targetEventId,
      operationId: retirementOperationId(binding, targetEventId),
      at: sweep.at,
    }));
    if (signal) checkCancellation(signal);
    if (
      !(await save(
        {
          ...binding,
          operations,
          next: page.next && { ...page.next },
        },
        signal,
      ))
    )
      throw new HeimdallError("conflict");
  }

  async runQueuedRetirement(
    bearer: string,
    batch: RetirementBatch,
    index: number,
    signal?: AbortSignal,
  ) {
    if (!batch?.sweep || !Number.isInteger(index) || !batch.operations?.[index])
      throw new HeimdallError("invalid_argument");
    const op = batch.operations[index];
    const scope = this.authentication.maintenanceScope();
    if (
      op.action !== "audit.retention.retire" ||
      op.tenantId !== batch.sweep.tenantId ||
      op.at !== batch.sweep.at ||
      op.operationId !== retirementOperationId(batch, op.targetEventId) ||
      batch.productId !== scope.productId ||
      batch.environment !== scope.environment ||
      batch.policyVersion !== scope.policyVersion ||
      batch.modelId !== this.modelId ||
      batch.manifestVersion !== this.manifestVersion ||
      batch.retainForMs !== this.retainForMs
    )
      throw new HeimdallError("conflict");
    return this.run(bearer, op, signal);
  }

  async run(bearer: string, operation: Operation, signal?: AbortSignal) {
    if (!operation) throw new HeimdallError("invalid_argument");
    const { at, tenantId, action, targetEventId, operationId } = operation;
    const now = this.now();
    if (!Number.isSafeInteger(now) || !Number.isSafeInteger(at) || at < 0 || at > now)
      throw new HeimdallError("invalid_argument");
    return this.authentication.runMaintenance(
      bearer,
      {
        tenantId,
        action,
        targetEventId,
        operationId,
        modelId: this.modelId,
        manifestVersion: this.manifestVersion,
      },
      this.authorize,
      (scope, signal) => {
        const current = this.now();
        if (!Number.isSafeInteger(current) || at > current) throw new HeimdallError("unavailable");
        const { productId, environment, tenantId, policyVersion } = scope;
        const request = {
          productId,
          environment,
          tenantId,
          policyVersion,
          eventId: scope.targetEventId,
          at,
        };
        const event = {
          schemaVersion: 1,
          eventType: "heimdall.policy.completed",
          eventVersion: 1,
          occurredAt: at,
          recordedAt: at,
          productId,
          environment,
          tenantId,
          actor: { kind: "user", ...scope.actor },
          target: { type: "audit_event", id: scope.targetEventId },
          action: scope.action,
          outcome: "succeeded",
          requestId: scope.operationId,
          source: "sdk",
          sdkVersion: "0.0.0",
          attributes: {},
          operationId: scope.operationId,
          policyVersion,
        };
        if (scope.action === "audit.retention.retire") {
          const r = { ...request, retirementId: scope.operationId, retainForMs: this.retainForMs };
          return this.store.retire(r, { ...event, eventId: auditRetirementEventId(r) }, signal);
        }
        if (!Object.hasOwn(holdActions, scope.action)) throw new HeimdallError("invalid_argument");
        const [purpose, operation] = holdActions[scope.action as keyof typeof holdActions];
        const r = { ...request, holdId: scope.operationId, purpose, operation };
        return this.store.applyHold(r, { ...event, eventId: auditHoldEventId(r) }, signal);
      },
      signal,
    );
  }
}
