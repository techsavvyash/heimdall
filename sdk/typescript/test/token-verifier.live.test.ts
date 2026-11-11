import { expect, test } from "vitest";
import { TokenVerifier } from "../src/internal/token-verifier.js";
test.skipIf(!process.env.HEIMDALL_TEST_TOKEN_CASES)(
  "live pinned-provider tokens",
  async () => {
    const cases = JSON.parse(process.env.HEIMDALL_TEST_TOKEN_CASES!);
    const verifier = new TokenVerifier({
      issuer: cases.issuer,
      audience: cases.audience,
      jwksUri: cases.jwksUri,
      profile: "production",
    });
    const valid = await verifier.verify(cases.token);
    expect(valid.subject).toBe(cases.subject);
    expect(verifier.owns(valid)).toBe(true);
    const other = new TokenVerifier({
      issuer: cases.issuer,
      audience: cases.otherAudience,
      jwksUri: cases.jwksUri,
      profile: "production",
    });
    await expect(other.verify(cases.token)).rejects.toThrow("unauthenticated");
    for (const forged of cases.forged)
      await expect(verifier.verify(forged)).rejects.toThrow("unauthenticated");
  },
  30000,
);
