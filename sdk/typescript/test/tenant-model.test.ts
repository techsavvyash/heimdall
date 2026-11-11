import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { compileTenantModel } from "../src/internal/tenant-model.js";
const cases = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/tenant-model-v1.json", import.meta.url),
    "utf8",
  ),
) as Array<{
  name: string;
  manifest: Parameters<typeof compileTenantModel>[0];
  binding: Parameters<typeof compileTenantModel>[1];
  expected: ReturnType<typeof compileTenantModel>;
}>;
it.each(cases)("compiles portable $name model and hash", (c) => {
  expect(compileTenantModel(c.manifest, c.binding)).toEqual(c.expected);
  const reordered = {
    ...c.manifest,
    permissions: [...c.manifest.permissions].reverse(),
    roles: Object.fromEntries(Object.entries(c.manifest.roles).reverse()),
  };
  expect(compileTenantModel(reordered, c.binding)).toEqual(c.expected);
  expect(() => compileTenantModel({ ...c.manifest, ownerRole: "missing" }, c.binding)).toThrow(
    "invalid_argument",
  );
});
