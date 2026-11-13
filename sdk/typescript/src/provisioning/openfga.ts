import type { Pool } from "pg";
import { parseDeployment } from "../internal/configuration.js";
import { HeimdallError } from "../internal/errors.js";
import { PinnedModel } from "../internal/model-binding.js";
import { setupJournal } from "./journal.js";
import { providerId, providerRequest } from "./provider-request.js";

type Options = {
  scope: Parameters<typeof setupJournal>[1];
  configJSON: string;
  manifestJSON: string;
  token: string;
  existingStoreId?: string;
  existingModelId?: string;
  recoverStoreId?: string;
  recoverModelId?: string;
};

export async function provisionOpenFGA(pool: Pool, options: Options, signal: AbortSignal) {
  const { scope, token, existingStoreId, existingModelId, recoverStoreId, recoverModelId } =
    options;
  const { config, manifest, artifact } = parseDeployment(options.configJSON, options.manifestJSON);
  const { url } = config.openfga;
  const profile = config.transportProfile ?? "production";
  if (
    config.productId !== scope.productId ||
    config.environment !== scope.environment ||
    scope.stepCount !== Object.keys(manifest.clients).length + 3 ||
    [existingStoreId, existingModelId, recoverStoreId, recoverModelId].some(
      (id) => id !== undefined && !providerId(id),
    ) ||
    (existingModelId && !existingStoreId) ||
    (existingStoreId && recoverStoreId) ||
    (existingModelId && recoverModelId)
  )
    throw new HeimdallError("invalid_argument");
  const call = providerRequest(url, token, profile, signal);
  const journal = setupJournal(pool, scope);
  const storeOrdinal = scope.stepCount - 1;
  if (recoverStoreId) await journal.requireDispatch(storeOrdinal, signal);
  const store = await journal.claim(storeOrdinal, signal);
  // Keep the name below OpenFGA's limit and bind it to the persisted dispatch.
  const name = "heimdall-" + store.dispatchId;
  const storeId = await setupResourceId(
    store,
    existingStoreId,
    recoverStoreId,
    async () => (await call<{ id: string }>("/stores", { name }))?.id,
  );
  const observed = await call<{ id: string; name: string }>(`/stores/${storeId}`);
  if (observed?.id !== storeId || (!existingStoreId && observed.name !== name))
    throw new HeimdallError("conflict", store.dispatchId);
  if (store.state !== "confirmed")
    await journal.confirm(storeOrdinal, store.dispatchId, storeId, signal);

  if (recoverModelId) await journal.requireDispatch(scope.stepCount, signal);
  const model = await journal.claim(scope.stepCount, signal);
  const modelId = await setupResourceId(
    model,
    existingModelId,
    recoverModelId,
    async () =>
      (
        await call<{ authorization_model_id: string }>(
          `/stores/${storeId}/authorization-models`,
          artifact.model,
        )
      )?.authorization_model_id,
  );
  await new PinnedModel(
    { url, storeId, modelId, profile },
    manifest,
    config,
    artifact,
    async () => token,
  ).verify(signal);
  if (model.state !== "confirmed")
    await journal.confirm(scope.stepCount, model.dispatchId, modelId, signal);
  return { url, storeId, modelId };
}

async function setupResourceId(
  step: Awaited<ReturnType<ReturnType<typeof setupJournal>["claim"]>>,
  existing: string | undefined,
  recover: string | undefined,
  create: () => Promise<string | undefined>,
) {
  const conflict = () => new HeimdallError("conflict", step.dispatchId);
  const unknown = () => new HeimdallError("outcome_unknown", step.dispatchId);
  if (step.state === "claimed" && recover) throw conflict();
  let id = step.resourceId ?? existing ?? recover;
  if (!id && step.state === "claimed") {
    try {
      id = await create();
    } catch {
      throw unknown();
    }
  }
  if (!providerId(id)) throw unknown();
  if ((existing && existing !== id) || (recover && recover !== id)) throw conflict();
  return id;
}
