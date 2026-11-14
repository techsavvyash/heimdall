import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { parseDeployment } from "../src/internal/configuration.js";
import { PinnedModel } from "../src/internal/model-binding.js";
import type { compileTenantModel } from "../src/internal/tenant-model.js";
it.skipIf(!process.env.HEIMDALL_TEST_MODEL_BINDING)("verifies the live pinned model", async () => {
  const config = JSON.parse(process.env.HEIMDALL_TEST_MODEL_BINDING!);
  const cases = JSON.parse(
    readFileSync(
      new URL("../../../contracts/fixtures/tenant-model-v1.json", import.meta.url),
      "utf8",
    ),
  ) as {
    name: string;
    manifest: Parameters<typeof compileTenantModel>[0];
    binding: Parameters<typeof compileTenantModel>[1];
    expected: ReturnType<typeof compileTenantModel>;
  }[];
  const fixture = cases.find((c) => c.name === config.name)!;
  const base = JSON.parse(
    readFileSync(
      new URL("../../../contracts/fixtures/configuration-v1.json", import.meta.url),
      "utf8",
    ),
  ).config;
  const deployment = parseDeployment(
    JSON.stringify({
      ...base,
      productId: fixture.manifest.productId,
      manifestVersion: fixture.binding.manifestVersion,
      clientKey: fixture.binding.clientKey,
      openfga: { url: config.url, storeId: config.storeId, modelId: config.modelId },
    }),
    JSON.stringify(fixture.manifest),
  );
  const model = new PinnedModel(
    { ...deployment.config.openfga, profile: deployment.config.transportProfile ?? "production" },
    deployment.manifest,
    deployment.config,
    deployment.artifact,
    async () => process.env.HEIMDALL_PROBE_OPENFGA_KEY!,
  );
  expect((await model.verify()).modelHash).toBe(fixture.expected.modelHash);
});
