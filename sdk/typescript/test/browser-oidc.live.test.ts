import { IdentityVerifier } from "../src/internal/identity-verifier.js";
import { discoverOIDC } from "../src/internal/oidc-discovery.js";
import { expect, test } from "vitest";
import { createBrowserOIDC } from "../src/internal/browser-oidc.js";

test.skipIf(!process.env.HEIMDALL_TEST_BROWSER_EXCHANGE)(
  "exchanges a fresh pinned-provider authorization code",
  async () => {
    const { config, code, nonce, pkceVerifier, subject } = JSON.parse(
      process.env.HEIMDALL_TEST_BROWSER_EXCHANGE!,
    );
    const browser = await createBrowserOIDC(config);
    const result = await browser.exchange(code, async () => ({ nonce, pkceVerifier }));
    expect(result.identity.subject === subject).toBe(true);
    expect(result.accessExpiresAt > Date.now()).toBe(true);
    expect(Boolean(result.refreshToken)).toBe(true);
    const metadata = await discoverOIDC(config.issuer, config.profile);
    const status = new IdentityVerifier(
      {
        ...config,
        jwksUri: metadata.jwks_uri,
        productId: "probe",
        environment: "test",
        policyVersion: "p1",
      },
      async () => config.clientSecret,
      { check: async () => false, invalidate: async () => {} },
    );
    expect((await status.verify(result.accessToken)).subject === subject).toBe(true);
    const refreshed = await browser.refresh(result.refreshToken!);
    expect((await status.verify(refreshed.accessToken)).subject === subject).toBe(true);
    expect(refreshed.refreshToken !== result.refreshToken).toBe(true);
  },
  30000,
);
