import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import { validateManifestBinding } from "../src/internal/manifest.js";

const root = new URL("../../../contracts/", import.meta.url);
const read = (path: string): unknown => JSON.parse(readFileSync(new URL(path, root), "utf8"));
type Vector = {
  name: string;
  manifest: Parameters<typeof validateManifestBinding>[0];
  config: Parameters<typeof validateManifestBinding>[1];
  valid: boolean;
};
const cases = read("fixtures/manifest-v1.json") as Vector[];
const ajv = new Ajv2020({ strict: true });
const manifestShape = ajv.compile(read("manifest.schema.json") as object);
const configShape = ajv.compile(read("config.schema.json") as object);

describe("manifest cross-reference contract", () => {
  it.each(cases)("has structurally valid inputs: $name", (c) => {
    expect(manifestShape(c.manifest)).toBe(true);
    expect(configShape(c.config)).toBe(true);
  });
  it.each(cases.filter((c) => c.valid))("accepts $name", (c) => {
    expect(() => validateManifestBinding(c.manifest, c.config)).not.toThrow();
  });
  it.each(cases.filter((c) => !c.valid))("rejects $name", (c) => {
    expect(() => validateManifestBinding(c.manifest, c.config)).toThrow("invalid_argument");
  });
});
