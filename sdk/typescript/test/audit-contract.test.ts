import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { describe, expect, it } from "vitest";
import { auditTypes, validateAuditEvent } from "../src/internal/audit.js";
const root = new URL("../../../contracts/", import.meta.url);
const read = (path: string): unknown => JSON.parse(readFileSync(new URL(path, root), "utf8"));
type Case = {
  name: string;
  event: Parameters<typeof validateAuditEvent>[0];
  origin: "sdk" | "product";
  valid: boolean;
};
const cases = read("fixtures/audit-v1.json") as Case[];
const registry = read("fixtures/audit-registry-v1.json") as Record<
  string,
  { version: number; schema: object }
>;
const ajv = new Ajv2020({ strict: true });
const shape = ajv.compile(read("audit-event.schema.json") as object);
const registered = new Map(
  Object.entries(registry).map(([name, def]) => [
    name,
    { version: def.version, validate: ajv.compile(def.schema) },
  ]),
);
const custom = (name: string, version: number, attrs: Record<string, unknown>): boolean => {
  const definition = registered.get(name);
  return definition !== undefined && definition.version === version && definition.validate(attrs);
};
describe("audit envelope semantics", () => {
  it("matches the shared reserved event catalog", () =>
    expect(auditTypes).toEqual(read("fixtures/audit-types-v1.json")));
  it.each(cases)("structural precondition $name", (c) => expect(shape(c.event)).toBe(true));
  it.each(cases.filter((c) => c.valid))("accepts $name", (c) => {
    expect(() => validateAuditEvent(c.event, c.origin, custom)).not.toThrow();
  });
  it.each(cases.filter((c) => !c.valid))("rejects $name", (c) => {
    expect(() => validateAuditEvent(c.event, c.origin, custom)).toThrow("invalid_argument");
  });
});
