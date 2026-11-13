import { randomUUID } from "node:crypto";
import type { Pool } from "pg";
import { bounded, known } from "../internal/audited-decision.js";
import { parseDeployment } from "../internal/configuration.js";
import { checkCancellation, HeimdallError } from "../internal/errors.js";
import { OperationRecords } from "../internal/postgres/operations.js";
import { providerId } from "./provider-request.js";
import { ensureTenantOwner } from "./tenant-owner.js";

export type WorkspaceRecoveryScope = Readonly<{
  productId: string;
  environment: string;
  operationId: string;
  workspaceId: string;
  generation: number;
  updatedAt: number;
  storeId: string;
  modelId: string;
}>;

export type WorkspaceRecoveryOptions = {
  operationId: string;
  initiatingActor: { issuer: string; subject: string };
  expectedGeneration: number;
  policyVersion: string;
  auditSinks: readonly string[];
  recoveryToken: string;
  /** Authorize the operator and verify quiescence of every old writer for this generation. */
  verifyQuiescence(
    scope: WorkspaceRecoveryScope,
    signal: AbortSignal,
  ): Promise<{
    operatorId: string;
    evidenceId: string;
  } | null>;
};

/** Trusted maintenance entry point. Keep the deployment drained through the entire call. */
export async function recoverWorkspaceCreation(
  pool: Pool,
  configJSON: string,
  manifestJSON: string,
  options: WorkspaceRecoveryOptions,
  caller: AbortSignal,
) {
  const { config, artifact } = parseDeployment(configJSON, manifestJSON);
  const { operationId, expectedGeneration, policyVersion, recoveryToken, verifyQuiescence } =
    options;
  const actor = { ...options.initiatingActor };
  if (
    !(caller instanceof AbortSignal) ||
    !providerId(operationId) ||
    !providerId(policyVersion) ||
    !Number.isSafeInteger(expectedGeneration) ||
    expectedGeneration < 1 ||
    actor.issuer !== config.issuer ||
    typeof verifyQuiescence !== "function"
  )
    throw new HeimdallError("invalid_argument");
  const signal = AbortSignal.any([caller, AbortSignal.timeout(30_000)]);
  const binding = {
    productId: config.productId,
    environment: config.environment,
    storeId: config.openfga.storeId,
    modelId: config.openfga.modelId,
    modelHash: artifact.modelHash,
    manifestVersion: config.manifestVersion,
  };
  const records = OperationRecords.forRetainedRuntime(pool, binding, options.auditSinks);
  let op = await records.get(operationId, actor, signal);
  if (!op || op.intent.kind !== "create_workspace") throw new HeimdallError("not_found");
  if (op.generation !== expectedGeneration || !["uncertain", "dispatching"].includes(op.state))
    throw new HeimdallError("conflict", operationId);
  const scope = Object.freeze({
    productId: binding.productId,
    environment: binding.environment,
    operationId,
    workspaceId: op.workspaceId,
    generation: op.generation,
    updatedAt: op.updatedAt,
    storeId: binding.storeId,
    modelId: binding.modelId,
  });
  let proof;
  try {
    proof = await bounded(
      (s) => verifyQuiescence(scope, s),
      AbortSignal.any([signal, AbortSignal.timeout(5000)]),
    );
  } catch (error) {
    throw known(error);
  }
  checkCancellation(signal);
  const { operatorId, evidenceId } = proof ?? {};
  if (!providerId(operatorId) || !providerId(evidenceId)) throw new HeimdallError("forbidden");
  const advance = (before: NonNullable<typeof op>, transition: string, s: AbortSignal) => {
    const at = Math.max(Date.now(), before.updatedAt);
    return records.advance(
      operationId,
      actor,
      before.generation,
      transition,
      {
        eventId: randomUUID(),
        requestId: evidenceId,
        policyVersion,
        occurredAt: at,
        recordedAt: at,
        nextAttemptAt: at,
        recoveryOperator: operatorId,
      },
      s,
    );
  };
  // Reserve the new generation before any provider I/O. A lost claim response cannot dispatch.
  op = await advance(op, "recover", signal);
  const quarantine = new HeimdallError("outcome_unknown", operationId);
  try {
    await ensureTenantOwner(
      configJSON,
      manifestJSON,
      recoveryToken,
      {
        operationId,
        tenantId: op.workspaceId,
        owner: op.intent.target,
      },
      async (s) => {
        checkCancellation(s);
      },
      signal,
    );
  } catch (error) {
    if (known(error).code === "conflict") throw new HeimdallError("conflict", operationId);
    throw quarantine;
  }
  op = await advance(op, "confirm", signal);
  op = await advance(op, "complete", signal);
  return { operationId, workspaceId: op.workspaceId, status: "active" as const };
}
