import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
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
  const model = new PinnedModel(
    { ...config, profile: "production" },
    fixture.manifest,
    fixture.binding,
    fixture.expected,
    async () => process.env.HEIMDALL_PROBE_OPENFGA_KEY!,
  );
  expect((await model.verify()).modelHash).toBe(fixture.expected.modelHash);
});
