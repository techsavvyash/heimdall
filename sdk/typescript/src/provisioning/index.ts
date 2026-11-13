import { createHash } from "node:crypto";
import type { Pool } from "pg";
import { parseDeployment } from "../internal/configuration.js";
import { HeimdallError } from "../internal/errors.js";
import { provisionApplication } from "./application.js";
import { provisionOpenFGA } from "./openfga.js";
import { provisionProject } from "./project.js";
import { providerId, providerRequest } from "./provider-request.js";

export type ProductSetupPlan = {
  productId: string;
  environment: string;
  issuer: string;
  organizationId: string;
  projectName: string;
  openfgaUrl: string;
  transportProfile?: "production" | "loopback";
  existing?: {
    projectId?: string;
    applications?: Readonly<Record<string, string>>;
    storeId?: string;
    modelId?: string;
  };
};

export type ProductSetupOptions = {
  zitadelToken: string;
  openfgaToken: string;
  saveSecret: Parameters<typeof provisionApplication>[1]["saveSecret"];
  hasSecret: Parameters<typeof provisionApplication>[1]["hasSecret"];
  recoverStoreId?: string;
  recoverModelId?: string;
};

/** Provision one product/environment using an explicitly migrated operator database. */
export async function setupProduct(
  pool: Pool,
  plan: ProductSetupPlan,
  manifestJSON: string,
  options: ProductSetupOptions,
  signal: AbortSignal,
) {
  const { productId, environment, issuer, organizationId, projectName, openfgaUrl } = plan;
  const profile = plan.transportProfile ?? "production";
  const { projectId, storeId, modelId } = plan.existing ?? {};
  const applications = { ...plan.existing?.applications };
  const { zitadelToken, openfgaToken, saveSecret, hasSecret, recoverStoreId, recoverModelId } =
    options;
  if (
    !(signal instanceof AbortSignal) ||
    typeof pool?.connect !== "function" ||
    !providerId(organizationId) ||
    typeof projectName !== "string" ||
    !projectName.isWellFormed() ||
    !projectName.trim() ||
    [...projectName].length > 200 ||
    typeof saveSecret !== "function" ||
    typeof hasSecret !== "function" ||
    [
      projectId,
      storeId,
      modelId,
      recoverStoreId,
      recoverModelId,
      ...Object.values(applications),
    ].some((id) => id !== undefined && !providerId(id)) ||
    (modelId && !storeId) ||
    (storeId && recoverStoreId) ||
    (modelId && recoverModelId) ||
    (Object.keys(applications).length > 0 && !projectId) ||
    new Set(Object.values(applications)).size !== Object.keys(applications).length ||
    typeof manifestJSON !== "string" ||
    Buffer.byteLength(manifestJSON) > 1048576
  )
    throw new HeimdallError("invalid_argument");
  // Validate both provider endpoints and credentials before the first write.
  providerRequest(issuer, zitadelToken, profile, signal);
  providerRequest(openfgaUrl, openfgaToken, profile, signal);
  let raw: { clients: Record<string, unknown>; version: number };
  try {
    raw = JSON.parse(manifestJSON);
  } catch {
    throw new HeimdallError("invalid_argument");
  }
  if (!raw?.clients || typeof raw.clients !== "object") throw new HeimdallError("invalid_argument");
  const clients = Object.keys(raw.clients).sort();
  const seed = {
    schemaVersion: 1,
    productId,
    environment,
    issuer,
    audience: "pending",
    clientId: "pending",
    zitadelProjectId: "pending",
    clientKey: clients[0],
    manifestVersion: raw.version,
    transportProfile: profile,
    openfga: { url: openfgaUrl, storeId: "pending", modelId: "pending" },
  };
  const { manifest, artifact } = parseDeployment(JSON.stringify(seed), manifestJSON);
  if (
    Buffer.byteLength(JSON.stringify(artifact)) > 1048576 ||
    Object.keys(applications).some((key) => !Object.hasOwn(manifest.clients, key)) ||
    clients.some((key) => (manifest.clients[key]!.transportProfile ?? "production") !== profile)
  )
    throw new HeimdallError("invalid_argument");
  const fingerprint = createHash("sha256")
    .update(
      JSON.stringify([
        "heimdall.setup.v1",
        productId,
        environment,
        issuer,
        organizationId,
        projectName,
        openfgaUrl,
        profile,
        createHash("sha256").update(manifestJSON).digest("hex"),
        projectId ?? "",
        storeId ?? "",
        modelId ?? "",
        clients.map((key) => [key, applications[key] ?? ""]),
      ]).replace(/\u2028|\u2029/g, (c) => (c === "\u2028" ? "\\u2028" : "\\u2029")),
    )
    .digest("hex");
  const scope = { productId, environment, fingerprint, stepCount: clients.length + 3 };
  const id = await provisionProject(
    pool,
    {
      scope,
      issuer,
      organizationId,
      name: projectName,
      token: zitadelToken,
      transportProfile: profile,
      existingProjectId: projectId,
    },
    signal,
  );
  const entries = [];
  for (const [index, key] of clients.entries()) {
    const app = await provisionApplication(
      pool,
      {
        scope,
        ordinal: index + 2,
        issuer,
        token: zitadelToken,
        projectId: id,
        name: key,
        redirectUris: manifest.clients[key]!.redirectUris,
        transportProfile: profile,
        existingApplicationId: applications[key],
        saveSecret,
        hasSecret,
      },
      signal,
    );
    entries.push([
      key,
      { ...seed, clientKey: key, clientId: app.clientId, audience: id, zitadelProjectId: id },
    ] as const);
  }
  const openfga = await provisionOpenFGA(
    pool,
    {
      scope,
      configJSON: JSON.stringify(entries[0]![1]),
      manifestJSON,
      token: openfgaToken,
      existingStoreId: storeId,
      existingModelId: modelId,
      recoverStoreId,
      recoverModelId,
    },
    signal,
  );
  return Object.fromEntries(entries.map(([key, config]) => [key, { ...config, openfga }]));
}

export { readSetupStatus } from "./journal.js";

export { ensureTenantOwner } from "./tenant-owner.js";
export type { TenantOwnerInput } from "./tenant-owner.js";

export { recoverWorkspaceCreation, recoverMembership } from "./workspace-recovery.js";
export type { WorkspaceRecoveryOptions, WorkspaceRecoveryScope } from "./workspace-recovery.js";
