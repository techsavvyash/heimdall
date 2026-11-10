import { readFileSync, readdirSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";

const root = new URL("../../../contracts/", import.meta.url);
const readJSON = (relative: string): unknown =>
  JSON.parse(readFileSync(new URL(relative, root), "utf8"));
const files = readdirSync(root).filter((name) => name.endsWith(".schema.json"));
type SchemaCase = { name: string; value: unknown; valid: boolean };

describe("portable JSON schemas", () => {
  it("contains schemas", () => expect(files.length).toBeGreaterThan(0));
  describe.each(files)("%s", (file) => {
    const name = file.replace(".schema.json", "");
    const ajv = new Ajv2020({ strict: true, allErrors: true });
    const validate = ajv.compile(readJSON(file) as object);
    const cases = readJSON("fixtures/schemas/" + name + ".json") as SchemaCase[];

    it("has positive and negative fixtures", () => {
      expect(cases.some((c) => c.valid)).toBe(true);
      expect(cases.some((c) => !c.valid)).toBe(true);
    });
    it.each(cases)("$name", (c) => {
      expect(validate(c.value)).toBe(c.valid);
    });
  });
});
