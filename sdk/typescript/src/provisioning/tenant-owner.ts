import { parseDeployment } from "../internal/configuration.js";
import { HeimdallError, checkCancellation } from "../internal/errors.js";
import { encodeIdentity } from "../internal/identity-encoding.js";
import { PinnedModel } from "../internal/model-binding.js";
import { bounded, known } from "../internal/audited-decision.js";
import { providerId, providerRequest } from "./provider-request.js";

export type TenantOwnerInput = {
  operationId: string;
  tenantId: string;
  owner: { issuer: string; subject: string };
};
type Tuple = { user: string; object: string; relation: string; condition?: unknown };

/** Run only under the caller's serialized pending-intent workflow. */
export async function ensureTenantOwner(
  configJSON: string,
  manifestJSON: string,
  token: string,
  input: TenantOwnerInput,
  beforeWrite: (signal: AbortSignal) => Promise<void>,
  signal: AbortSignal,
) {
  const { config, manifest, artifact } = parseDeployment(configJSON, manifestJSON);
  const { operationId, tenantId } = input;
  const { issuer, subject } = input.owner;
  if (
    !providerId(operationId) ||
    !providerId(tenantId) ||
    issuer !== config.issuer ||
    typeof beforeWrite !== "function" ||
    !(signal instanceof AbortSignal)
  )
    throw new HeimdallError("invalid_argument");
  const user = encodeIdentity(issuer, subject);
  const { url, storeId, modelId } = config.openfga;
  const profile = config.transportProfile ?? "production";
  const operation = AbortSignal.any([signal, AbortSignal.timeout(20_000)]);
  const call = providerRequest(url, token, profile, operation);
  const verified = await new PinnedModel(
    { url, storeId, modelId, profile },
    manifest,
    config,
    artifact,
    async () => token,
  ).verify(operation);
  const relation = verified.roles[manifest.ownerRole]!.assignment;
  const object = "workspace:" + tenantId;
  const owner = { user, relation, object };
  const member = { user, relation: "member", object };

  async function read(tuple: Tuple, allUsers: boolean) {
    const page = await call<{ tuples: { key: Tuple }[]; continuation_token?: string }>(
      `/stores/${storeId}/read`,
      {
        tuple_key: allUsers ? { object, relation: tuple.relation } : tuple,
        consistency: "HIGHER_CONSISTENCY",
        page_size: 2,
      },
    );
    if (
      !page ||
      !Array.isArray(page.tuples) ||
      page.tuples.some(
        (entry) => entry?.key?.relation !== tuple.relation || entry.key.object !== object,
      )
    )
      throw new HeimdallError("unavailable", operationId);
    if (allUsers && (page.tuples.length > 1 || page.tuples.some(({ key }) => key.user !== user)))
      throw new HeimdallError("conflict", operationId);
    if (
      (page.continuation_token !== undefined && page.continuation_token !== "") ||
      page.tuples.length > 1 ||
      page.tuples.some(
        ({ key }) => key.user !== user || (key.condition !== undefined && key.condition !== null),
      )
    )
      throw new HeimdallError("unavailable", operationId);
    return page.tuples.length === 1;
  }

  const missing: Tuple[] = [];
  if (!(await read(owner, true))) missing.push(owner);
  if (!(await read(member, false))) missing.push(member);
  if (missing.length) {
    // The workflow must commit the audit intent and dispatch marker before returning.
    try {
      await bounded(beforeWrite, operation);
    } catch (error) {
      throw new HeimdallError(known(error).code, operationId);
    }
    try {
      checkCancellation(operation);
      const response = await call(`/stores/${storeId}/write`, {
        authorization_model_id: modelId,
        writes: { tuple_keys: missing, on_duplicate: "ignore" },
      });
      if (!response || !(await read(owner, true)) || !(await read(member, false)))
        throw new Error();
    } catch {
      throw new HeimdallError("outcome_unknown", operationId);
    }
  }
  return { operationId, tenantId, principal: user, storeId, modelId };
}
