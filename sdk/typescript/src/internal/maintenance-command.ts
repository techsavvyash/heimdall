import { auditHoldEventId, type AuditHoldRequest } from "./audit-hold.js";
import { auditRetirementEventId, type AuditRetirementRequest } from "./audit-retire.js";
import type { AuditedAuthentication } from "./audited-authentication.js";
import { HeimdallError } from "./errors.js";

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
