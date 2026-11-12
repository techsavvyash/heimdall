import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { validateAuditHold, type AuditHoldRequest } from "../src/internal/audit-hold.js";
const cases = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-hold-binding.json", import.meta.url),
    "utf8",
  ),
) as {
  name: string;
  request: AuditHoldRequest;
  administration: Record<string, unknown>;
  valid: boolean;
}[];
test.each(cases)("hold administration binding: $name", (c) => {
  const before = structuredClone(c);
  const result = (() => {
    try {
      validateAuditHold(c.request, c.administration);
      return "valid";
    } catch (e) {
      return (e as Error).message;
    }
  })();
  expect(result).toBe(c.valid ? "valid" : "invalid_argument");
  expect(c).toEqual(before);
});
test("hold time rejects fractional and nonfinite values", () => {
  const c = cases[0]!;
  for (const at of [0.5, NaN, Infinity])
    expect(() => validateAuditHold({ ...c.request, at }, c.administration)).toThrow(
      "invalid_argument",
    );
});
