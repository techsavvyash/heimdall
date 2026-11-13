import { randomUUID } from "node:crypto";
import type { Pool } from "pg";
import { parseDeployment } from "../internal/configuration.js";
import { checkCancellation, HeimdallError } from "../internal/errors.js";
import { decodeIdentity } from "../internal/identity-encoding.js";
import { PinnedModel } from "../internal/model-binding.js";
import { OperationRecords } from "../internal/postgres/operations.js";
import { WorkspaceRecords } from "../internal/postgres/workspaces.js";
import { providerId } from "./provider-request.js";

export type DiscoveryRebuildCheckpoint = Readonly<{
  productId: string;
  environment: string;
  workspaceId: string;
  storeId: string;
  modelId: string;
  manifestVersion: number;
  phase: "remote" | "candidates";
  position: string;
}>;
export type DiscoveryRebuildOptions = {
  workspaceId: string;
  policyVersion: string;
  openfgaToken: string;
  auditSinks: readonly string[];
  checkpoint?: DiscoveryRebuildCheckpoint;
};

/** Trusted operator call. Persist the returned checkpoint only after success. */
export async function rebuildDiscovery(
  pool: Pool,
  configJSON: string,
  manifestJSON: string,
  options: DiscoveryRebuildOptions,
  caller: AbortSignal,
): Promise<{ processed: number; checkpoint: DiscoveryRebuildCheckpoint | null }> {
  const { config, manifest, artifact } = parseDeployment(configJSON, manifestJSON);
  const { workspaceId, policyVersion, openfgaToken } = options;
  if (
    !(caller instanceof AbortSignal) ||
    !providerId(workspaceId) ||
    !providerId(policyVersion) ||
    typeof openfgaToken !== "string" ||
    !/^[\x21-\x7e]{1,8192}$/.test(openfgaToken)
  )
    throw new HeimdallError("invalid_argument");
  const scope = {
    productId: config.productId,
    environment: config.environment,
    workspaceId,
    storeId: config.openfga.storeId,
    modelId: config.openfga.modelId,
    manifestVersion: config.manifestVersion,
  };
  const prior = options.checkpoint;
  const phase = prior?.phase ?? "remote",
    position = prior?.position ?? "";
  if (
    prior &&
    (Object.entries(scope).some(([key, value]) => prior[key as keyof typeof scope] !== value) ||
      typeof position !== "string" ||
      (phase === "remote"
        ? !position || Buffer.byteLength(position) > 8192
        : phase !== "candidates" ||
          !/^(0|[1-9][0-9]{0,18})$/.test(position) ||
          BigInt(position) > 9223372036854775807n))
  )
    throw new HeimdallError("invalid_argument");
  const signal = AbortSignal.any([caller, AbortSignal.timeout(30000)]);
  checkCancellation(signal);
  const workspaces = WorkspaceRecords.forRetainedRuntime(pool, scope.productId, scope.environment);
  const records = OperationRecords.forRetainedRuntime(
    pool,
    { ...scope, modelHash: artifact.modelHash },
    options.auditSinks,
  );
  const before = await workspaces.get(workspaceId, signal);
  if (!before) throw new HeimdallError("not_found");
  if (before.lifecycle !== "active" || before.mutationBlocked) throw new HeimdallError("conflict");
  const model = new PinnedModel(
    { ...config.openfga, profile: config.transportProfile ?? "production" },
    manifest,
    config,
    artifact,
    async () => openfgaToken,
  );
  await model.verify(signal);
  const observedAt = Math.max(Date.now(), before.updatedAt);
  const observations: {
    identity: ReturnType<typeof decodeIdentity>;
    member: boolean;
    observedAt: number;
  }[] = [];
  function identity(user: string) {
    const value = decodeIdentity(user);
    if (value.issuer !== config.issuer) throw new HeimdallError("unavailable");
    return value;
  }
  let checkpoint: DiscoveryRebuildCheckpoint | null;
  if (phase === "remote") {
    const page = await model.readDirectMembers(workspaceId, position || null, signal);
    for (const user of page.users)
      observations.push({ identity: identity(user), member: true, observedAt });
    checkpoint = page.next
      ? { ...scope, phase, position: page.next }
      : { ...scope, phase: "candidates", position: "0" };
  } else {
    const page = await records.candidateRepairPage(workspaceId, position, signal);
    for (const row of page.items) {
      const decoded = identity(row.principal);
      const member = await model.checkMembership(row.principal, workspaceId, signal);
      observations.push({ identity: decoded, member, observedAt });
    }
    checkpoint = page.more ? { ...scope, phase, position: page.items.at(-1)!.position } : null;
  }
  if (observations.length) {
    await records.repairCandidates(
      workspaceId,
      before.revision,
      observations,
      {
        eventId: randomUUID(),
        requestId: randomUUID(),
        policyVersion,
        occurredAt: observedAt,
        recordedAt: Math.max(Date.now(), observedAt),
      },
      signal,
    );
  }
  checkCancellation(signal);
  return { processed: observations.length, checkpoint };
}
