import { expect, test } from "vitest";
import { IdentityVerifier } from "../src/internal/identity-verifier.js";
test.skipIf(!process.env.HEIMDALL_TEST_STATUS_CASE)(
  "live authoritative identity status",
  async () => {
    const c = JSON.parse(process.env.HEIMDALL_TEST_STATUS_CASE!);
    const negatives: string[] = [];
    const verifier = new IdentityVerifier(
      {
        issuer: c.issuer,
        audience: c.audience,
        jwksUri: c.jwksUri,
        profile: "production",
        clientId: c.clientId,
        productId: c.product,
        environment: "verification",
        policyVersion: "live-v1",
        freshnessMs: 0,
      },
      async () => c.clientSecret,
      {
        check: async () => false,
        invalidate: async (n) => {
          negatives.push(n.reason);
        },
      },
    );
    const result = await verifier.verify(c.token, "live").then(
      (p) => ({ result: p.kind, subject: p.subject, owned: verifier.owns(p) }),
      (e) => ({ result: e.code }),
    );
    expect(result).toEqual(
      c.expected === "human"
        ? { result: "human", subject: c.subject, owned: true }
        : { result: "unauthenticated" },
    );
    expect(negatives).toEqual(c.expected === "human" ? [] : ["token_inactive"]);
  },
  30000,
);
