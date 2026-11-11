import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { bindAuditDecision } from "../src/internal/audit-decision-binding.js";
const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-decision-binding.json", import.meta.url),
    "utf8",
  ),
);
for (const c of fixture.cases)
  c.events = c.events.map(
    (spec: { base: string; patch?: Record<string, unknown>; omit?: string[] }) => {
      const event = { ...structuredClone(fixture.base[spec.base]), ...spec.patch };
      for (const key of spec.omit ?? []) delete event[key];
      return event;
    },
  );
for (const c of fixture.cases.filter((c: { valid: boolean }) => c.valid))
  test(`decision binding ${c.name}`, () => {
    const before = JSON.stringify(c.events);
    const result = bindAuditDecision(fixture.productId, fixture.environment, c.events);
    expect(result).toEqual(c.expected);
    expect(JSON.stringify(c.events)).toBe(before);
  });
for (const c of fixture.cases.filter((c: { valid: boolean }) => !c.valid))
  test(`decision binding refuses ${c.name}`, () => {
    expect(() => bindAuditDecision(fixture.productId, fixture.environment, c.events)).toThrow(
      "audit_unavailable",
    );
  });
test("decision binding validates configured scope", () => {
  expect(() => bindAuditDecision("ledger\n", "production", fixture.cases[0].events)).toThrow(
    "invalid_argument",
  );
});
