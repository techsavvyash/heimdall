import { expect, test } from "vitest";
import { createBrowserLoginHTTP } from "../src/internal/browser-login-http.js";
import { customSessionStore } from "../src/internal/custom-session-store.js";
import { BrowserLoginRecords } from "../src/internal/postgres/login.js";
import { memorySessionStore } from "./fixtures/memory-session-store.js";

test.skipIf(!process.env.HEIMDALL_TEST_FEDERATION_HTTP)(
  "executes a browser-bound federation callback against pinned ZITADEL",
  async () => {
    const input = JSON.parse(process.env.HEIMDALL_TEST_FEDERATION_HTTP!);
    const adapter = await createBrowserLoginHTTP(
      {
        ...input.config,
        productId: "federation",
        environment: "verification",
        returnToKey: "default",
      },
      new BrowserLoginRecords(
        customSessionStore("federation", "verification", {
          store: memorySessionStore().store,
          activeKeyId: "probe",
          keys: { probe: Buffer.alloc(32, 7) },
          reserveKeyUsage: async () => {},
        }),
      ),
    );
    const start = await adapter.startFederation(
      new Request(input.config.redirectUri),
      "acme",
      input.policy,
      input.credential,
    );
    const authorization = new URL(start.headers.get("location")!);
    expect(authorization.origin).toBe(input.fixtureOrigin);
    const bridge = async (path: string, body: object) =>
      fetch(input.bridge + path, {
        method: "POST",
        headers: { "content-type": "application/json", authorization: "Bearer " + input.bridgeKey },
        body: JSON.stringify(body),
        redirect: "error",
        signal: AbortSignal.timeout(15000),
      });
    const result = await bridge("/upstream", { authorizationUrl: authorization.href });
    expect(result.status).toBe(200);
    const callback = new URL((await result.json()).callback);
    const cookie = start.headers.getSetCookie()[0]!.split(";")[0]!;
    const complete = (u: URL, value = cookie) =>
      adapter.federationCallback(
        new Request(u, { headers: { cookie: value } }),
        "acme",
        input.policy,
        input.credential,
      );
    await expect(complete(callback, "")).rejects.toThrow("unauthenticated");
    const changed = new URL(callback);
    changed.searchParams.set("id", "different-intent");
    await expect(complete(changed)).rejects.toThrow("unauthenticated");
    await expect(
      adapter.federationCallback(
        new Request(callback, { headers: { cookie } }),
        "acme",
        { ...input.policy, policyVersion: "rotated" },
        input.credential,
      ),
    ).rejects.toThrow("unauthenticated");
    const verified = await complete(callback);
    expect(
      (await bridge("/session", { sessionId: verified.evidence.providerSessionId })).status,
    ).toBe(204);
    expect(verified.evidence).toMatchObject({
      subject: input.subject,
      tenantId: "acme",
      providerId: input.policy.providerId,
      organizationId: input.policy.organizationId,
      protocol: input.policy.protocol,
    });
    await expect(complete(callback)).rejects.toThrow("unauthenticated");
  },
  45000,
);
