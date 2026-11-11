import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import {
  parseDeployment,
  validateDeploymentShape,
  validateManifestShape,
} from "../src/internal/configuration.js";
const read = (path: string) =>
  JSON.parse(readFileSync(new URL(`../../../contracts/fixtures/${path}`, import.meta.url), "utf8"));
for (const [name, validate] of [
  ["config", validateDeploymentShape],
  ["manifest", validateManifestShape],
] as const) {
  const cases = read(`schemas/${name}.json`) as { name: string; value: unknown; valid: boolean }[];
  it.each(cases)(`${name} structure: $name`, (c) => {
    let valid = true;
    try {
      validate(c.value);
    } catch {
      valid = false;
    }
    expect(valid).toBe(c.valid);
  });
}
const f = read("configuration-v1.json");
type Patch = [string[], unknown];
function apply(value: Record<string, unknown>, patches: Patch[] = []) {
  for (const [path, replacement] of patches) {
    let object = value;
    for (const key of path.slice(0, -1)) object = object[key] as Record<string, unknown>;
    object[path.at(-1)!] = replacement;
  }
  return value;
}
it.each(f.cases as { name: string; valid: boolean; config?: Patch[]; manifest?: Patch[] }[])(
  "deployment binding: $name",
  (c) => {
    const config = apply(structuredClone(f.config), c.config);
    const manifest = apply(structuredClone(f.manifest), c.manifest);
    let valid = true;
    try {
      parseDeployment(JSON.stringify(config), JSON.stringify(manifest));
    } catch {
      valid = false;
    }
    expect(valid).toBe(c.valid);
  },
);
it("produces a frozen isolated compiler artifact", () => {
  const result = parseDeployment(JSON.stringify(f.config), JSON.stringify(f.manifest));
  expect(result.artifact).toEqual(read("tenant-model-v1.json")[0].expected);
  expect(() => {
    result.config.openfga.modelId = "changed";
  }).toThrow(TypeError);
  expect(() => {
    result.manifest.roles.viewer!.push("changed");
  }).toThrow(TypeError);
  expect(() => {
    result.artifact.permissions["invoice.read"] = "member";
  }).toThrow(TypeError);
});
it.each(["{", "[]", "null", " ".repeat(1048577)])(
  "rejects malformed or oversized config",
  (value) => {
    expect(() => parseDeployment(value, JSON.stringify(f.manifest))).toThrow("invalid_argument");
  },
);
it("accepts integer JSON exponent spelling and rejects unpaired Unicode", () => {
  const config = JSON.stringify(f.config);
  expect(
    parseDeployment(
      config.replace('"schemaVersion":1', '"schemaVersion":1e0'),
      JSON.stringify(f.manifest),
    ).config.schemaVersion,
  ).toBe(1);
  expect(() =>
    parseDeployment(config.replace('"ledger-api"', '"\\ud800"'), JSON.stringify(f.manifest)),
  ).toThrow("invalid_argument");
});
it("redacts rejected configuration details", () => {
  expect(() =>
    parseDeployment(
      JSON.stringify({ ...f.config, secret: "do-not-log" }),
      JSON.stringify(f.manifest),
    ),
  ).toThrow(/^invalid_argument$/);
});
