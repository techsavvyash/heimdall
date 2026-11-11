import { expect, test } from "vitest";
import { createHeimdall, auditEventDigest as auditDigest } from "../src/index.js";
test.skipIf(!process.env.HEIMDALL_TEST_RUNTIME)(
  "live public runtime with recording storage fixture",
  async () => {
    const c = JSON.parse(process.env.HEIMDALL_TEST_RUNTIME!);
    const events: Record<string, unknown>[] = [];
    const runtime = await createHeimdall(
      JSON.stringify(c.config),
      JSON.stringify(c.manifest),
      {
        policyVersion: "runtime-v1",
        identityFreshnessMs: 0,
        credentials: {
          clientSecret: async () => c.clientSecret,
          openfgaKey: async () => process.env.HEIMDALL_PROBE_OPENFGA_KEY!,
        },
        storage: {
          productId: c.config.productId,
          environment: c.config.environment,
          ready: async () => {},
          identity: { check: async () => false, invalidate: async () => {} },
          appendAudit: async (e) => {
            events.push(e);
            return {
              productId: e.productId as string,
              environment: e.environment as string,
              eventId: e.eventId as string,
              recordedAt: e.recordedAt as number,
              payloadDigest: auditDigest(e),
            };
          },
        },
      },
      { signal: new AbortController().signal },
    );
    const principal = await runtime.tokens.verify(c.token, {
      signal: new AbortController().signal,
    });
    expect(principal.subject).toBe(c.subject);
    expect(principal.productId).toBe(c.config.productId);
    await expect(
      runtime.tokens.verify("invalid", { signal: new AbortController().signal }),
    ).rejects.toThrow("unauthenticated");
    expect(events.map((e) => e.outcome)).toEqual(["succeeded", "deny"]);
  },
  30000,
);
