import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import { transitionAuditCapacity } from "../src/internal/audit-capacity.js";
type Args = Parameters<typeof transitionAuditCapacity>;
const cases = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/audit-capacity.json", import.meta.url), "utf8"),
) as Array<{
  name: string;
  limits: Args[0];
  state: Args[1];
  operation: Args[2];
  charge: Args[3];
  expected: Args[1] | string;
}>;
it.each(cases)("audit capacity: $name", ({ limits, state, operation, charge, expected }) => {
  const original = structuredClone({ limits, state, charge });
  const result = (() => {
    try {
      return transitionAuditCapacity(limits, state, operation, charge);
    } catch (error) {
      return (error as Error).message;
    }
  })();
  expect(result).toEqual(expected);
  expect({ limits, state, charge }).toEqual(original);
});
it("refuses noninteger limits and malformed reservation vectors", () => {
  const item = cases[0]!;
  for (const events of [NaN, Infinity, 1.5, Number.MAX_SAFE_INTEGER + 1])
    expect(() =>
      transitionAuditCapacity({ ...item.limits, events }, item.state, item.operation, item.charge),
    ).toThrow("invalid_argument");
  expect(() =>
    transitionAuditCapacity(
      item.limits,
      {
        used: { events: 1, envelopeBytes: 1, deliveries: 0 },
        reserved: { events: 2, envelopeBytes: 1, deliveries: 64 },
      },
      "complete",
      item.charge,
    ),
  ).toThrow("audit_unavailable");
});
