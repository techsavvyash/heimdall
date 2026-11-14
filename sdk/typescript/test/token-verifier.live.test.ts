import { expect, test } from "vitest";
import { discoverOIDC } from "../src/internal/oidc-discovery.js";
import { TokenVerifier } from "../src/internal/token-verifier.js";
test.skipIf(!process.env.HEIMDALL_TEST_TOKEN_CASES)(
  "live pinned-provider tokens",
  async () => {
    const cases = JSON.parse(process.env.HEIMDALL_TEST_TOKEN_CASES!);
    const metadata = await discoverOIDC(cases.issuer, "production");
    expect(metadata.jwks_uri).toBe(cases.jwksUri);
    const verifier = new TokenVerifier({
      issuer: cases.issuer,
      audience: cases.audience,
      jwksUri: metadata.jwks_uri,
      profile: "production",
    });
    const valid = await verifier.verify(cases.token);
    expect(valid.subject).toBe(cases.subject);
    expect(verifier.owns(valid)).toBe(true);
    const other = new TokenVerifier({
      issuer: cases.issuer,
      audience: cases.otherAudience,
      jwksUri: metadata.jwks_uri,
      profile: "production",
    });
    await expect(other.verify(cases.token)).rejects.toThrow("unauthenticated");
    for (const forged of cases.forged)
      await expect(verifier.verify(forged)).rejects.toThrow("unauthenticated");
  },
  30000,
);
