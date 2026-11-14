import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { expect, test } from "vitest";
import { validateAuditShape } from "../src/internal/audit-shape.js";
const read = (name: string) =>
  JSON.parse(readFileSync(new URL("../../../contracts/" + name, import.meta.url), "utf8"));
const schema = read("audit-event.schema.json");
const shape = new Ajv2020({ strict: true }).compile(schema);
const cases = read("fixtures/schemas/audit-event.json") as {
  name: string;
  value: unknown;
  valid: boolean;
}[];
test.each(cases)("bundled audit schema $name", (c) => {
  let valid = true;
  try {
    validateAuditShape(c.value);
  } catch {
    valid = false;
  }
  expect(valid).toBe(c.valid);
});
const base = cases.find((c) => c.valid)!.value as Record<string, unknown>;
for (const key of Object.keys(schema.properties)) {
  test(`audit shape matches schema for ${key}`, () => {
    for (const value of [
      null,
      true,
      false,
      {},
      [],
      [1],
      0,
      1,
      -1,
      1.5,
      2147483648,
      9007199254740991,
      "",
      "valid",
      "user",
      "unknown",
      "system",
      "succeeded",
      "heimdall.test",
      "heimdall.test\n",
      "id\n",
      "東京",
      "🧭".repeat(1024),
      "x".repeat(8193),
    ]) {
      const event = { ...base, [key]: value };
      let valid = true;
      try {
        validateAuditShape(event);
      } catch {
        valid = false;
      }
      expect(valid).toBe(shape(event));
    }
  });
}
test("every required field and nested extra is rejected", () => {
  for (const key of schema.required) {
    const event = { ...base };
    delete event[key];
    expect(() => validateAuditShape(event)).toThrow("invalid_argument");
  }
  for (const key of ["actor", "target"]) {
    expect(() =>
      validateAuditShape({ ...base, [key]: { ...(base[key] as object), extra: 1 } }),
    ).toThrow("invalid_argument");
  }
  expect(() => validateAuditShape({ ...base, extra: 1 })).toThrow("invalid_argument");
});
test.each(
  read("fixtures/audit-shape-boundaries.json") as {
    name: string;
    path: string[];
    value?: unknown;
    repeat?: string;
    count?: number;
    valid: boolean;
  }[],
)("audit shape boundary $name", (c) => {
  const event = structuredClone(base);
  let target = event;
  for (const key of c.path.slice(0, -1)) target = target[key] as Record<string, unknown>;
  target[c.path.at(-1)!] = c.repeat === undefined ? c.value : c.repeat.repeat(c.count!);
  let valid = true;
  try {
    validateAuditShape(event);
  } catch {
    valid = false;
  }
  expect(valid).toBe(c.valid);
  expect(shape(event)).toBe(c.valid);
});
