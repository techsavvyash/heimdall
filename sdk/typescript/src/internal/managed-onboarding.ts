import { randomUUID } from "node:crypto";
import type { Principal } from "../runtime.js";
import { ensureTenantOwner } from "../provisioning/tenant-owner.js";
import type { AuditedAuthentication } from "./audited-authentication.js";
import { bounded, known } from "./audited-decision.js";
import type { parseDeployment } from "./configuration.js";
import { checkCancellation, HeimdallError } from "./errors.js";
import { intentDigests, type Intent } from "./operations.js";
import type { ManagedOperation, OperationRecords } from "./postgres/operations.js";

export type WorkspaceCreateInput = Readonly<{ name: string; idempotencyKey: string }>;
export type WorkspaceCreationPolicy = (
  actor: Principal,
  workspace: Readonly<{ name: string }>,
  signal: AbortSignal,
) => Promise<boolean>;

export type WorkspaceOnboardingStatus = Readonly<{
  operationId: string;
  workspaceId: string;
  status: "active" | "pending" | "cancelled";
  reason?: "retryable" | "recovery_required";
}>;
type Call = { signal: AbortSignal };
export type WorkspaceOnboarding = Readonly<{
  create(
    actor: Principal,
    input: WorkspaceCreateInput,
    call: Call,
  ): Promise<WorkspaceOnboardingStatus>;
  get(actor: Principal, operationId: string, call: Call): Promise<WorkspaceOnboardingStatus>;
  resume(actor: Principal, operationId: string, call: Call): Promise<WorkspaceOnboardingStatus>;
}>;

function status(op: ManagedOperation): WorkspaceOnboardingStatus {
  return {
    operationId: op.operationId,
    workspaceId: op.workspaceId,
    status:
      op.state === "completed" ? "active" : op.state === "cancelled" ? "cancelled" : "pending",
    ...(op.state === "dispatching" || op.state === "uncertain"
      ? { reason: "recovery_required" }
      : op.state === "retryable"
        ? { reason: "retryable" }
        : {}),
  };
}

export function managedOnboarding(
  configJSON: string,
  manifestJSON: string,
  config: ReturnType<typeof parseDeployment>["config"],
  policyVersion: string,
  records: OperationRecords,
  auth: AuditedAuthentication,
  key: (signal: AbortSignal) => Promise<string>,
  policy?: WorkspaceCreationPolicy,
): WorkspaceOnboarding {
  function request(actor: Principal, call: Call) {
    if (!(call?.signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
    checkCancellation(call.signal);
    if (!auth.owns(actor)) throw new HeimdallError("unauthenticated");
    return { signal: call.signal, identity: { issuer: actor.issuer, subject: actor.subject } };
  }
  function metadata(requestId: string, floor = 0) {
    const at = Math.max(Date.now(), floor);
    return {
      eventId: randomUUID(),
      requestId,
      policyVersion,
      occurredAt: at,
      recordedAt: at,
      nextAttemptAt: at,
    };
  }
  async function drive(op: ManagedOperation, signal: AbortSignal) {
    const requestId = randomUUID();
    const actor = op.intent.actor;
    async function advance(transition: string, operation: AbortSignal) {
      op = await records.advance(
        op.operationId,
        actor,
        op.generation,
        transition,
        metadata(requestId, op.updatedAt),
        operation,
      );
    }
    try {
      if (op.state === "retryable") {
        if (Date.now() < op.nextAttemptAt) return status(op);
        await advance("retry", signal);
      }
      if (op.state === "pending") {
        let dispatchStarted = false;
        let dispatchCommitted = false;
        async function dispatch(operation: AbortSignal) {
          dispatchStarted = true;
          await advance("dispatch", operation);
          dispatchCommitted = true;
        }
        try {
          const token = await bounded(
            key,
            AbortSignal.any([signal, AbortSignal.timeout(5000)]),
          ).catch((error: unknown) => {
            throw known(error);
          });
          await ensureTenantOwner(
            configJSON,
            manifestJSON,
            token,
            { operationId: op.operationId, tenantId: op.workspaceId, owner: op.intent.target },
            dispatch,
            signal,
          );
        } catch (error) {
          const code = known(error).code;
          if (code === "outcome_unknown" && dispatchCommitted) {
            if (!signal.aborted) await advance("uncertain", signal);
            return status(op);
          }
          if (
            !dispatchStarted &&
            !signal.aborted &&
            (code === "unavailable" || code === "deadline_exceeded")
          ) {
            await advance("pre_dispatch_failure", signal);
            return status(op);
          }
          throw error;
        }
        // A verification-only attempt still needs its own generation before confirmation.
        if (!dispatchStarted) await dispatch(signal);
        await advance("confirm", signal);
      }
      if (op.state === "confirmed") await advance("complete", signal);
      return status(op);
    } catch (error) {
      if (known(error).code === "conflict" && !signal.aborted) {
        const current = await records.get(op.operationId, actor, signal);
        if (current && current.generation !== op.generation) return status(current);
      }
      throw error;
    }
  }
  async function observe(actor: Principal, id: string, call: Call, action: string) {
    const { signal, identity } = request(actor, call);
    let record: ManagedOperation | null = null;
    const allowed = await auth.checkPermission(
      actor,
      null,
      action,
      config.openfga.modelId,
      config.manifestVersion,
      async (operation) => {
        const found = await records.get(id, identity, operation);
        if (found?.intent.kind !== "create_workspace") return false;
        record = found;
        return true;
      },
      signal,
    );
    if (!allowed || !record) throw new HeimdallError("not_found");
    return { record, signal };
  }
  return Object.freeze({
    async create(actor: Principal, input: WorkspaceCreateInput, call: Call) {
      const { signal, identity } = request(actor, call);
      const intent: Intent = {
        schemaVersion: 1,
        kind: "create_workspace",
        productId: config.productId,
        environment: config.environment,
        manifestVersion: config.manifestVersion,
        actor: identity,
        target: identity,
        workspaceId: "",
        name: input?.name,
        roles: [],
      };
      const key = input?.idempotencyKey;
      intentDigests(intent, key);
      let existing: ManagedOperation | null = null;
      const allowed = await auth.checkPermission(
        actor,
        null,
        "workspace.create",
        config.openfga.modelId,
        config.manifestVersion,
        async (operation) => {
          existing = await records.findIntent(intent, key, operation);
          return (
            !!existing ||
            (policy !== undefined &&
              (await policy(actor, Object.freeze({ name: intent.name }), operation)) === true)
          );
        },
        signal,
      );
      if (!allowed) throw new HeimdallError("forbidden");
      if (existing) return status(existing);
      const operationId = randomUUID();
      const op = await records.create(
        JSON.stringify(intent),
        key,
        {
          operationId,
          workspaceId: randomUUID(),
          ...metadata(randomUUID()),
        },
        signal,
      );
      return op.operationId === operationId ? drive(op, signal) : status(op);
    },
    async get(actor: Principal, id: string, call: Call) {
      const { record } = await observe(actor, id, call, "workspace.operation.read");
      return status(record);
    },
    async resume(actor: Principal, id: string, call: Call) {
      const { record, signal } = await observe(actor, id, call, "workspace.operation.resume");
      return drive(record, signal);
    },
  });
}
