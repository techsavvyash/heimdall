import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import { validateStorageCompatibility } from "../src/internal/storage-contract.js";
const read = (file: string): unknown =>
  JSON.parse(readFileSync(new URL(`../../../contracts/${file}`, import.meta.url), "utf8"));
const shape = new Ajv2020({ strict: true }).compile(read("storage-metadata.schema.json") as object);
const cases = read("fixtures/storage-compatibility-v1.json") as {
  name: string;
  metadata: Parameters<typeof validateStorageCompatibility>[0];
  policy: Parameters<typeof validateStorageCompatibility>[1];
  error?: string;
}[];
describe("storage compatibility", () => {
  it.each(cases)("metadata schema $name", (c) => expect(shape(c.metadata)).toBe(true));
  it.each(cases.filter((c) => !c.error))("accepts $name", (c) =>
    expect(() => validateStorageCompatibility(c.metadata, c.policy)).not.toThrow(),
  );
  it.each(cases.filter((c) => c.error))("rejects $name", (c) =>
    expect(() => validateStorageCompatibility(c.metadata, c.policy)).toThrow(c.error),
  );
});
