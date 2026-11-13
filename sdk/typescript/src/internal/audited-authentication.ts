import { maintenanceTargetType } from "./audit.js";
import { validateAuditShape } from "./audit-shape.js";
import { randomUUID } from "node:crypto";
import { IdentityVerifier } from "./identity-verifier.js";
import { AuditedDecision, bounded, commitAuditEvent, known } from "./audited-decision.js";
import { checkCancellation, HeimdallError } from "./errors.js";

type Arguments = ConstructorParameters<typeof IdentityVerifier>;
type Principal = Awaited<ReturnType<IdentityVerifier["verify"]>>;
type MaintenanceRequest = {
  tenantId: string | null;
  action: string;
  targetEventId: string;
  operationId: string;
  modelId: string;
  manifestVersion: number;
};
export type MaintenanceContext = Readonly<
  MaintenanceRequest & {
    productId: string;
    environment: string;
    policyVersion: string;
    actor: Readonly<{ issuer: string; subject: string }>;
  }
>;
type ExportKind = "workspaces" | "operations";
type ManagedExportScan = (
  kind: ExportKind,
  workspaceId: string | null,
  after: string | null,
  limit: number,
  signal: AbortSignal,
) => Promise<string>;
// Internal composition. Identity metadata comes only from this captured verifier.
export class AuditedAuthentication {
  private readonly verifier: IdentityVerifier;
  private readonly productId: string;
  private readonly environment: string;
  private readonly policyVersion: string;
  private readonly releaseWindow: number;
  constructor(
    config: Arguments[0],
    secret: Arguments[1],
    gate: Arguments[2],
    private readonly append: Parameters<typeof commitAuditEvent>[1],
    private readonly now: () => number = Date.now,
  ) {
    if (
      ![config.productId, config.environment].every((v) => /^[A-Za-z0-9_-]{1,64}$/.test(v)) ||
      typeof append !== "function"
    )
      throw new HeimdallError("invalid_argument");
    this.verifier = new IdentityVerifier(config, secret, gate, now);
    this.productId = config.productId;
    this.environment = config.environment;
    this.policyVersion = config.policyVersion;
    this.releaseWindow = config.freshnessMs === 0 ? 5000 : (config.freshnessMs ?? 60000);
  }
  maintenanceScope() {
    return {
      productId: this.productId,
      environment: this.environment,
      policyVersion: this.policyVersion,
    };
  }
  owns(value: unknown): boolean {
    return this.verifier.owns(value);
  }
  // Internal boundary. The deployment supplies read-only policy and the trusted port.
  async runMaintenance(
    raw: string,
    request: MaintenanceRequest,
    policy: (scope: MaintenanceContext, signal: AbortSignal) => Promise<boolean>,
    apply: (
      scope: MaintenanceContext,
      signal?: AbortSignal,
    ) => ReturnType<Parameters<typeof commitAuditEvent>[1]>,
    signal?: AbortSignal,
  ) {
    if (typeof apply !== "function") throw new HeimdallError("invalid_argument");
    return this.runOperatorAction(
      raw,
      request,
      "audit_event",
      policy,
      (scope) => apply(scope, signal),
      signal,
    );
  }

  private async runOperatorAction<T>(
    raw: string,
    request: MaintenanceRequest,
    targetType: string,
    policy: (scope: MaintenanceContext, signal: AbortSignal) => Promise<boolean>,
    apply: (scope: MaintenanceContext, checkCurrent: () => Promise<void>) => Promise<T>,
    signal?: AbortSignal,
  ): Promise<T> {
    if (!request) throw new HeimdallError("invalid_argument");
    const { tenantId, action, targetEventId, operationId, modelId, manifestVersion } = request;
    const r = { tenantId, action, targetEventId, operationId, modelId, manifestVersion };
    const token = (v: unknown) =>
      typeof v === "string" && /^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(v);
    if (
      maintenanceTargetType(r.action) !== targetType ||
      (r.tenantId !== null && !token(r.tenantId)) ||
      ![r.targetEventId, r.operationId, r.modelId].every(token) ||
      !Number.isInteger(r.manifestVersion) ||
      r.manifestVersion < 1 ||
      r.manifestVersion > 2147483647 ||
      typeof policy !== "function" ||
      typeof apply !== "function"
    )
      throw new HeimdallError("invalid_argument");
    if (signal) checkCancellation(signal);
    const principal = await this.verify(raw, "bearer", signal);
    const actor = Object.freeze({ issuer: principal.issuer, subject: principal.subject });
    const scope = Object.freeze({
      ...r,
      productId: this.productId,
      environment: this.environment,
      policyVersion: this.policyVersion,
      actor,
    });
    const at = this.now();
    const decision = new AuditedDecision(
      this.productId,
      this.environment,
      this.append,
      validateAuditShape,
      this.now,
    );
    const allowed = await decision.run(
      {
        schemaVersion: 1,
        eventId: "pending",
        eventType: "heimdall.authorization.requested",
        eventVersion: 1,
        occurredAt: at,
        recordedAt: at,
        productId: this.productId,
        environment: this.environment,
        tenantId: r.tenantId,
        actor: { kind: "user", ...actor },
        target: { type: targetType, id: r.targetEventId },
        action: r.action,
        permission: r.action,
        outcome: "requested",
        requestId: r.operationId,
        source: "sdk",
        sdkVersion: "0.0.0",
        attributes: {},
        decisionId: "pending",
        operationId: r.operationId,
        modelId: r.modelId,
        manifestVersion: r.manifestVersion,
        policyVersion: this.policyVersion,
      },
      (signal) => policy(scope, signal),
      signal,
    );
    if (!allowed) throw new HeimdallError("forbidden");
    const checkCurrent = async () => {
      if (signal) checkCancellation(signal);
      await this.verifier.checkCurrent(principal, signal);
      if (signal) checkCancellation(signal);
    };
    await checkCurrent();
    return apply(scope, checkCurrent);
  }

  async runManagedExport(
    raw: string,
    request: {
      kind: "workspaces" | "operations";
      workspaceId: string | null;
      after: string | null;
      limit: number;
      operationId: string;
    },
    modelId: string,
    manifestVersion: number,
    policy: (scope: MaintenanceContext, signal: AbortSignal) => Promise<boolean>,
    scan: ManagedExportScan,
    signal?: AbortSignal,
  ): Promise<string> {
    if (!request) throw new HeimdallError("invalid_argument");
    const { kind, workspaceId, after, limit, operationId } = request;
    if (
      !["workspaces", "operations"].includes(kind) ||
      (kind === "operations" && workspaceId === null) ||
      (after !== null &&
        (typeof after !== "string" || !/^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(after))) ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100 ||
      typeof scan !== "function"
    )
      throw new HeimdallError("invalid_argument");
    const target = workspaceId ?? "inventory";
    return this.runOperatorAction(
      raw,
      {
        tenantId: workspaceId,
        action: "managed.export." + kind,
        targetEventId: target,
        operationId,
        modelId,
        manifestVersion,
      },
      "managed_export",
      policy,
      async (scope, checkCurrent) => {
        const started = this.now();
        const readSignal = AbortSignal.any([
          AbortSignal.timeout(5000),
          ...(signal ? [signal] : []),
        ]);
        let page = "";
        let failure: HeimdallError | undefined;
        try {
          page = await bounded(
            (readSignal) => scan(kind, workspaceId, after, limit, readSignal),
            readSignal,
          );
          checkCancellation(readSignal);
          const header = JSON.parse(page);
          if (
            header?.schemaVersion !== 1 ||
            header.productId !== scope.productId ||
            header.environment !== scope.environment ||
            header.kind !== kind ||
            header.workspaceId !== workspaceId
          )
            throw new HeimdallError("unavailable");
        } catch (error) {
          failure = known(error);
        }
        await commitAuditEvent(
          {
            schemaVersion: 1,
            eventId: randomUUID(),
            eventType: "heimdall.export.completed",
            eventVersion: 1,
            occurredAt: started,
            recordedAt: Math.max(started, this.now()),
            productId: scope.productId,
            environment: scope.environment,
            tenantId: scope.tenantId,
            actor: { kind: "user", ...scope.actor },
            target: { type: "managed_export", id: target },
            action: scope.action,
            outcome: failure ? "failed" : "succeeded",
            requestId: operationId,
            operationId,
            policyVersion: scope.policyVersion,
            source: "sdk",
            sdkVersion: "0.0.0",
            attributes: {},
            ...(failure ? { reasonCode: failure.code } : {}),
          },
          this.append,
          validateAuditShape,
        );
        if (failure) throw failure;
        await checkCurrent();
        return page;
      },
      signal,
    );
  }

  async runManagedInventory(
    raw: string,
    request: Parameters<AuditedAuthentication["runManagedExport"]>[1],
    modelId: string,
    manifestVersion: number,
    policy: Parameters<AuditedAuthentication["runManagedExport"]>[4],
    snapshot: (
      read: (scan: ManagedExportScan) => Promise<string>,
      signal: AbortSignal,
    ) => Promise<string>,
    signal?: AbortSignal,
  ): Promise<string> {
    if (request?.after !== null || typeof snapshot !== "function")
      throw new HeimdallError("invalid_argument");
    return this.runManagedExport(
      raw,
      request,
      modelId,
      manifestVersion,
      policy,
      (kind, workspaceId, _after, limit, signal) =>
        snapshot(async (scan) => {
          const items: unknown[] = [];
          let after: string | null = null;
          let size = 0;
          for (let index = 0; index < 100; index++) {
            const page = await scan(kind, workspaceId, after, limit, signal);
            size += Buffer.byteLength(page);
            if (size > 4 * 1024 * 1024) throw new HeimdallError("unavailable");
            const envelope = JSON.parse(page);
            if (
              envelope?.schemaVersion !== 1 ||
              envelope.productId !== this.productId ||
              envelope.environment !== this.environment ||
              envelope.kind !== kind ||
              envelope.workspaceId !== workspaceId ||
              !Array.isArray(envelope.items) ||
              envelope.items.length > limit
            )
              throw new HeimdallError("unavailable");
            items.push(...envelope.items);
            const next: unknown = envelope.nextId;
            if (next === null)
              return JSON.stringify({
                schemaVersion: 1,
                productId: this.productId,
                environment: this.environment,
                kind,
                workspaceId,
                items,
                nextId: null,
              });
            if (
              typeof next !== "string" ||
              !/^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(next) ||
              (after !== null && next <= after) ||
              envelope.items.length !== limit
            )
              throw new HeimdallError("unavailable");
            after = next;
          }
          throw new HeimdallError("unavailable");
        }, signal),
      signal,
    );
  }

  async verify(raw: string, context: string, caller?: AbortSignal): Promise<Principal> {
    const operation = AbortSignal.any([AbortSignal.timeout(5000), ...(caller ? [caller] : [])]);
    let principal: Principal | undefined;
    let failure: HeimdallError | undefined;
    try {
      principal = await bounded((signal) => this.verifier.verify(raw, context, signal), operation);
      checkCancellation(operation);
    } catch (error) {
      failure = known(error);
    }
    const outcome = failure
      ? failure.code === "cancelled"
        ? "cancelled"
        : failure.code === "invalid_argument"
          ? "invalid"
          : ["unauthenticated", "forbidden"].includes(failure.code)
            ? "deny"
            : "unavailable"
      : "succeeded";
    const recordedAt = this.now();
    const event = {
      schemaVersion: 1,
      eventId: randomUUID(),
      eventType: "heimdall.authentication.completed",
      eventVersion: 1,
      occurredAt: recordedAt,
      recordedAt,
      productId: this.productId,
      environment: this.environment,
      tenantId: null,
      actor: principal
        ? { kind: "user", issuer: principal.issuer, subject: principal.subject }
        : { kind: "unknown" },
      target: { type: "product", id: this.productId },
      action: "authenticate",
      outcome,
      requestId: randomUUID(),
      source: "sdk",
      sdkVersion: "0.0.0",
      policyVersion: this.policyVersion,
      attributes: {},
      ...(failure ? { reasonCode: failure.code } : {}),
    };
    await commitAuditEvent(event, this.append, validateAuditShape);
    if (failure) throw failure;
    try {
      checkCancellation(operation);
      const releasedAt = this.now();
      if (releasedAt >= principal!.expiresAt) throw new HeimdallError("unauthenticated");
      if (
        releasedAt < principal!.identityValidatedAt ||
        releasedAt - principal!.identityValidatedAt >= this.releaseWindow
      )
        throw new HeimdallError("unavailable");
    } catch (error) {
      const rejected = known(error);
      const at = Math.max(event.recordedAt, this.now());
      await commitAuditEvent(
        {
          ...event,
          eventId: randomUUID(),
          occurredAt: at,
          recordedAt: at,
          outcome:
            rejected.code === "cancelled"
              ? "cancelled"
              : rejected.code === "unauthenticated"
                ? "deny"
                : "unavailable",
          reasonCode: rejected.code,
        },
        this.append,
        validateAuditShape,
      );
      throw rejected;
    }
    return principal!;
  }
}
