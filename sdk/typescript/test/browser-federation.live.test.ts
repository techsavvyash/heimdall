import { createHash } from "node:crypto";
import { browserSession } from "../src/internal/browser-session.js";
import { AuditedAuthentication } from "../src/internal/audited-authentication.js";
import { auditDigest } from "../src/internal/audit-receipts.js";
import { expect, test } from "vitest";
import { createBrowserLoginHTTP } from "../src/internal/browser-login-http.js";
import { customSessionStore } from "../src/internal/custom-session-store.js";
import { BrowserLoginRecords } from "../src/internal/postgres/login.js";
import { memorySessionStore } from "./fixtures/memory-session-store.js";

test.skipIf(!process.env.HEIMDALL_TEST_FEDERATION_HTTP)(
  "executes a browser-bound federation callback against pinned ZITADEL",
  async () => {
    const input = JSON.parse(process.env.HEIMDALL_TEST_FEDERATION_HTTP!);
    const storage = customSessionStore("federation", "verification", {
      store: memorySessionStore().store,
      activeKeyId: "probe",
      keys: { probe: Buffer.alloc(32, 7) },
      reserveKeyUsage: async () => {},
    });
    const scope = { productId: "federation", environment: "verification" };
    const adapter = await createBrowserLoginHTTP(
      { ...input.config, ...scope, returnToKey: "default" },
      new BrowserLoginRecords(storage),
    );
    const metadata = await (
      await fetch(input.config.issuer + "/.well-known/openid-configuration")
    ).json();
    const audit: Record<string, unknown>[] = [];
    let policy = input.policy;
    const source = async () => policy;
    const auth = new AuditedAuthentication(
      { ...scope, ...input.config, jwksUri: metadata.jwks_uri, policyVersion: "probe-runtime" },
      async () => input.config.clientSecret,
      { check: async () => false, invalidate: async () => {} },
      async (event) => {
        audit.push(event);
        return {
          ...scope,
          eventId: event.eventId as string,
          recordedAt: event.recordedAt as number,
          payloadDigest: auditDigest(event),
        };
      },
      Date.now,
      source,
    );
    const sessions = browserSession(
      adapter,
      auth,
      storage,
      {
        returnTo: "/",
        federation: { credential: async () => input.credential },
      },
      Date.now,
      source,
    );
    const start = await sessions.startFederation(new Request(input.config.redirectUri), "acme");
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
      sessions.callback(new Request(u, { headers: { cookie: value } }));
    await expect(complete(callback, "")).rejects.toThrow("unauthenticated");
    const changed = new URL(callback);
    changed.searchParams.set("id", "different-intent");
    await expect(complete(changed)).rejects.toThrow("unauthenticated");
    policy = { ...input.policy, policyVersion: "rotated" };
    await expect(complete(callback)).rejects.toThrow("unauthenticated");
    policy = input.policy;
    const completed = await complete(callback);
    expect(completed.status).toBe(303);
    const sessionCookie = completed.headers.getSetCookie().at(-1)!.split(";")[0]!;
    const principal = await sessions.authenticate(
      new Request(input.config.redirectUri, { headers: { cookie: sessionCookie } }),
    );
    expect(principal.subject).toBe(input.subject);
    const evidence = (await storage.getSession(
      createHash("sha256").update(sessionCookie.split("=")[1]!).digest("base64url"),
    ))!;
    const decoded = JSON.parse(evidence.value.toString());
    expect(decoded.enterpriseEvidence).toMatchObject({
      subject: input.subject,
      tenantId: "acme",
      providerId: input.policy.providerId,
    });
    expect(
      (await bridge("/session", { sessionId: decoded.enterpriseEvidence.providerSessionId }))
        .status,
    ).toBe(204);
    expect(
      audit.filter((event) => event.eventType === "heimdall.authentication.completed"),
    ).toHaveLength(2);
    await expect(complete(callback)).rejects.toThrow("unauthenticated");
  },
  45000,
);
