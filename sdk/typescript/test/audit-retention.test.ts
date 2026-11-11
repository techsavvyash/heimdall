import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import {
  classifyAuditRetention,
  defaultAuditRetentionMs,
} from "../src/internal/audit-retention.js";
type Args = Parameters<typeof classifyAuditRetention>;
const cases = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-retention.json", import.meta.url),
    "utf8",
  ),
) as {
  name: string;
  scope: Args[0];
  group: Args[1];
  now: number;
  retainForMs: number;
  expected: string;
}[];
test.each(cases)("audit retention: $name", (c) => {
  const original = structuredClone(c);
  const result = (() => {
    try {
      return classifyAuditRetention(c.scope, c.group, c.now, c.retainForMs);
    } catch (error) {
      return (error as Error).message;
    }
  })();
  expect(result).toBe(c.expected);
  expect(c).toEqual(original);
});
test("default retention is thirty days and fractional times fail closed", () => {
  expect(defaultAuditRetentionMs).toBe(2592000000);
  const c = cases[0]!;
  expect(classifyAuditRetention(c.scope, c.group, c.now)).toBe("candidate");
  expect(() => classifyAuditRetention(c.scope, c.group, NaN)).toThrow("invalid_argument");
  expect(() => classifyAuditRetention(c.scope, c.group, c.now, 0.5)).toThrow("invalid_argument");
});
