import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { compileTenantModel, roleRelations } from "../src/internal/tenant-model.js";
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

it.each(cases)("resolves declared roles and rejects unknown names: $name", (c) => {
  const model = compileTenantModel(c.manifest, c.binding);
  for (const role of Object.keys(c.manifest.roles))
    expect(roleRelations(model, role)).toEqual(c.expected.roles[role]);
  for (const role of ["", "constructor", "__proto__", "OWNER", "unknown"])
    expect(() => roleRelations(model, role)).toThrow("invalid_argument");
  const invalid = {
    ...c.manifest,
    delegation: { ...c.manifest.delegation, [c.manifest.ownerRole]: [c.manifest.ownerRole] },
  };
  expect(() => compileTenantModel(invalid, c.binding)).toThrow("invalid_argument");
});
