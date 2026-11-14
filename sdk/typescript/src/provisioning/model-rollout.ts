import { isDeepStrictEqual } from "node:util";
import { parseDeployment } from "../internal/configuration.js";
import { HeimdallError, checkCancellation } from "../internal/errors.js";
import { PinnedModel } from "../internal/model-binding.js";
import { providerRequest } from "./provider-request.js";

type Deployment = { configJSON: string; manifestJSON: string };

/** Verify both published pins and report policy drift without writing provider state. */
export async function inspectModelRollout(
  current: Deployment,
  candidate: Deployment,
  token: string,
  signal: AbortSignal,
) {
  if (!(signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
  const old = parseDeployment(current?.configJSON, current?.manifestJSON);
  const next = parseDeployment(candidate?.configJSON, candidate?.manifestJSON);
  const a = old.config;
  const b = next.config;
  const profile = a.transportProfile ?? "production";
  if (
    a.productId !== b.productId ||
    a.environment !== b.environment ||
    a.issuer !== b.issuer ||
    a.audience !== b.audience ||
    a.zitadelProjectId !== b.zitadelProjectId ||
    a.openfga.url !== b.openfga.url ||
    a.openfga.storeId !== b.openfga.storeId ||
    a.openfga.modelId === b.openfga.modelId ||
    b.manifestVersion <= a.manifestVersion ||
    profile !== (b.transportProfile ?? "production")
  )
    throw new HeimdallError("invalid_argument");
  providerRequest(a.openfga.url, token, profile, signal);
  checkCancellation(signal);

  for (const { config, manifest, artifact } of [old, next]) {
    await new PinnedModel(
      { ...config.openfga, profile },
      manifest,
      config,
      artifact,
      async () => token,
    ).verify(signal);
  }
  const { manifest: previous, artifact } = old;
  const roles = Object.keys(previous.roles).sort();
  const permissions = [...previous.permissions].sort();
  const roleMappingChanged = roles.filter(
    (name) => !isDeepStrictEqual(artifact.roles[name], next.artifact.roles[name]),
  );
  const permissionMappingChanged = permissions.filter(
    (name) => artifact.permissions[name] !== next.artifact.permissions[name],
  );
  const grantingRoles = (manifest: typeof previous, permission: string) =>
    Object.keys(manifest.roles)
      .filter((role) => manifest.roles[role]!.includes(permission))
      .sort();
  const permissionPolicyChanged = permissions.filter(
    (name) => !isDeepStrictEqual(grantingRoles(previous, name), grantingRoles(next.manifest, name)),
  );
  const delegates = (manifest: typeof previous, target: string) =>
    Object.keys(manifest.delegation)
      .filter((role) => manifest.delegation[role]!.includes(target))
      .sort();
  const delegationChanged = roles.filter(
    (name) => !isDeepStrictEqual(delegates(previous, name), delegates(next.manifest, name)),
  );
  const ownerChanged = previous.ownerRole !== next.manifest.ownerRole;

  return {
    compatible: !(
      ownerChanged ||
      roleMappingChanged.length ||
      permissionMappingChanged.length ||
      permissionPolicyChanged.length ||
      delegationChanged.length
    ),
    ownerChanged,
    roleMappingChanged,
    permissionMappingChanged,
    permissionPolicyChanged,
    delegationChanged,
  };
}
