import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import type { MaintenanceContext } from "../src/internal/audited-authentication.js";
import { HeimdallError } from "../src/internal/errors.js";
import { createOperatorPolicy } from "../src/internal/operator-policy.js";

const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/operator-policy.json", import.meta.url),
    "utf8",
  ),
) as {
  config: Parameters<typeof createOperatorPolicy>[0];
  cases: (Partial<
    Pick<MaintenanceContext, "productId" | "environment" | "tenantId" | "action" | "policyVersion">
  > & {
    name: string;
    grant: number;
    issuer?: string;
    subject?: string;
    allow?: boolean;
    evidence?: string;
    error?: string;
  })[];
};

test.each(fixture.cases)("operator policy $name", async (c) => {
  const config = structuredClone(fixture.config);
  const grant = config.grants[c.grant]!;
  const { issuer, subject, action, tenantId } = grant;
  const { productId, environment, policyVersion } = config;
  let evidenceCalls = 0;
  const policy = createOperatorPolicy(config, async (scope) => {
    evidenceCalls++;
    if (scope.targetEventId !== "target" || scope.operationId !== "hold") throw new Error();
    if (c.evidence === "unavailable") throw new HeimdallError("unavailable");
    return c.evidence === "ended";
  });
  grant.subject = "modified";
  config.policyVersion = "modified";
  const result = await policy(
    {
      productId: c.productId ?? productId,
      environment: c.environment ?? environment,
      policyVersion: c.policyVersion ?? policyVersion,
      tenantId: c.tenantId ?? tenantId,
      actor: { issuer: c.issuer ?? issuer, subject: c.subject ?? subject },
      action: c.action ?? action,
      targetEventId: "target",
      operationId: "hold",
      modelId: "model",
      manifestVersion: 1,
    },
    new AbortController().signal,
  ).catch((error: HeimdallError) => error.code);
  expect(result).toBe(c.error ?? c.allow ?? false);
  expect(evidenceCalls).toBe(c.evidence ? 1 : 0);
});

test("refuses invalid configured grants", () => {
  const { config } = fixture;
  const grant = config.grants[0]!;
  for (const grants of [
    [grant, grant],
    [{ ...grant, action: "*" }],
    [{ ...grant, tenantId: "" }],
    [{ ...grant, action: "managed.export.operations" }],
    [{ ...grant, action: "audit.hold.retry.release" }],
  ]) {
    expect(() => createOperatorPolicy({ ...config, grants })).toThrow(HeimdallError);
  }
});
