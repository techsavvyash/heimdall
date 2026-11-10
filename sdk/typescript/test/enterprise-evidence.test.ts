import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import { validateEnterpriseEvidence } from "../src/internal/enterprise-evidence.js";
const root = new URL("../../../contracts/", import.meta.url);
const read = (name: string): unknown => JSON.parse(readFileSync(new URL(name, root), "utf8"));
const shape = new Ajv2020({ strict: true }).compile(
  read("enterprise-evidence.schema.json") as object,
);
const cases = read("fixtures/enterprise-evidence-v1.json") as {
  name: string;
  evidence: Parameters<typeof validateEnterpriseEvidence>[0];
  scope: Parameters<typeof validateEnterpriseEvidence>[1];
  now: number;
  maxAgeMs: number;
  error?: string;
}[];
describe("enterprise evidence target-policy gate", () => {
  it.each(cases.filter((c) => c.evidence !== null))("structural precondition $name", (c) =>
    expect(shape(c.evidence)).toBe(true),
  );
  it.each(cases.filter((c) => c.error))("rejects $name", (c) => {
    expect(() => validateEnterpriseEvidence(c.evidence, c.scope, c.now, c.maxAgeMs)).toThrow(
      c.error,
    );
  });
  it.each(cases.filter((c) => !c.error))("accepts $name", (c) => {
    expect(() => validateEnterpriseEvidence(c.evidence, c.scope, c.now, c.maxAgeMs)).not.toThrow();
  });
});
