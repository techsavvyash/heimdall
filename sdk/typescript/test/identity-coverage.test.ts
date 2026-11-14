import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { deriveIdentityEventCoverage, type IdentityCoveragePolicy } from "../src/postgres.js";

const cases = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/identity-coverage.json", import.meta.url),
    "utf8",
  ),
) as {
  name: string;
  nowMs: number;
  policy: IdentityCoveragePolicy;
  expected?: {
    retainedFromMicros: string | number;
    visibleBeforeMicros: string | number;
    validUntilMs: number;
  };
  error?: string;
}[];

test.each(cases)("$name", ({ policy, nowMs, expected, error }) => {
  let actual: unknown;
  try {
    const coverage = deriveIdentityEventCoverage(policy, nowMs);
    actual = {
      ...coverage,
      retainedFromMicros: String(coverage.retainedFromMicros),
      visibleBeforeMicros: String(coverage.visibleBeforeMicros),
    };
  } catch (failure) {
    actual = { error: failure instanceof Error ? failure.message : undefined };
  }
  expect(actual).toEqual(expected ?? { error });
});
