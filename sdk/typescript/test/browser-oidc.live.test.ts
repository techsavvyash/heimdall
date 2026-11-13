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
  },
  30000,
);
