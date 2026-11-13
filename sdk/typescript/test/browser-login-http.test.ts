import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { exportJWK, generateKeyPair, SignJWT } from "jose";
import { Pool } from "pg";
import { expect, test, vi } from "vitest";
import { createBrowserLoginHTTP } from "../src/internal/browser-login-http.js";
import { RecordCipher } from "../src/internal/envelopes.js";
import { BrowserLoginRecords } from "../src/internal/postgres/login.js";
import { PostgresRecords } from "../src/internal/postgres/records.js";

const fixture = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const f = fixture("browser-login-http");
const { issuer, metadata } = fixture("oidc-discovery");

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "Fetch callback uses its browser cookie and claims the PostgreSQL record once",
  async () => {
    const { privateKey, publicKey } = await generateKeyPair("RS256", { extractable: true });
    const jwks = { keys: [{ ...(await exportJWK(publicKey)), kid: "login-http" }] };
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const cipher = new RecordCipher("k1", { k1: Buffer.alloc(32, 7) });
    const records = new BrowserLoginRecords(
      new PostgresRecords(pool, pool, cipher, f.productId, f.environment),
    );
    const pending = new Map<string, string>();
    let exchanges = 0;
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      if (url.endsWith("/.well-known/openid-configuration")) return Response.json(metadata);
      if (url === metadata.jwks_uri) return Response.json(jwks);
      expect(url).toBe(metadata.token_endpoint);
      exchanges++;
      const body = init!.body as URLSearchParams;
      expect(body.get("redirect_uri")).toBe(f.redirectUri);
      const challenge = createHash("sha256").update(body.get("code_verifier")!).digest("base64url");
      const nonce = pending.get(challenge);
      expect(Boolean(nonce)).toBe(true);
      const sign = (aud: string, claims: Record<string, unknown>) =>
        new SignJWT(claims)
          .setProtectedHeader({ alg: "RS256", kid: "login-http" })
          .setIssuer(issuer)
          .setSubject("alice")
          .setAudience(aud)
          .setIssuedAt()
          .setExpirationTime("5m")
          .sign(privateKey);
      return Response.json({
        token_type: "Bearer",
        access_token: await sign(f.audience, {}),
        id_token: await sign(f.clientId, { nonce }),
      });
    });
    try {
      const http = await createBrowserLoginHTTP(
        { ...f, issuer, clientSecret: "fixture", profile: "production" },
        records,
      );
      const start = async () => {
        const response = await http.start(new Request(new URL("/auth/start", f.redirectUri)));
        expect(response.status).toBe(303);
        expect(response.headers.get("cache-control")).toBe("no-store");
        const url = new URL(response.headers.get("location")!);
        const cookie = response.headers.get("set-cookie")!;
        const state = url.searchParams.get("state")!;
        expect(url.origin + url.pathname).toBe(metadata.authorization_endpoint);
        expect(url.searchParams.get("code_challenge_method")).toBe("S256");
        expect(url.searchParams.get("scope")).toContain(
          `urn:zitadel:iam:org:project:id:${f.audience}:aud`,
        );
        expect(cookie).toMatch(/^__Host-heimdall-/);
        expect(cookie).toContain("Max-Age=600; Path=/; HttpOnly; SameSite=Lax; Secure");
        expect(cookie).not.toContain("Domain=");
        pending.set(url.searchParams.get("code_challenge")!, url.searchParams.get("nonce")!);
        return { state, cookie: cookie.split(";")[0]! };
      };
      const a = await start();
      const b = await start();
      const callback = (
        flow: typeof a,
        extra = "",
        cookie = flow.cookie,
        url = f.redirectUri,
        method = "GET",
      ) =>
        http.callback(
          new Request(`${url}?code=${f.code}&state=${flow.state}${extra}`, {
            method,
            headers: { cookie },
          }),
        );
      const rejects: Record<string, () => Promise<unknown>> = {
        "missing cookie": () => callback(a, "", ""),
        "other tab cookie": () => callback(a, "", b.cookie),
        "duplicate cookie": () => callback(a, "", `${a.cookie}; ${a.cookie}`),
        "duplicate state": () => callback(a, `&state=${a.state}`),
        "wrong issuer": () => callback(a, "&iss=https%3A%2F%2Fwrong.example.test"),
        "wrong host": () => callback(a, "", a.cookie, "https://other.example.test/auth/callback"),
        "wrong path": () => callback(a, "", a.cookie, f.redirectUri + "/other"),
        "wrong method": () => callback(a, "", a.cookie, f.redirectUri, "POST"),
      };
      for (const name of f.cases as string[])
        await expect(rejects[name]!()).rejects.toThrow("unauthenticated");
      expect(exchanges).toBe(0);
      const results = await Promise.allSettled([callback(a), callback(a), callback(b)]);
      const winners = results.flatMap((r) => (r.status === "fulfilled" ? [r.value] : []));
      expect(winners).toHaveLength(2);
      expect(winners.map((r) => r.tokens.identity.subject)).toEqual(["alice", "alice"]);
      expect(winners.map((r) => r.returnToKey)).toEqual([f.returnToKey, f.returnToKey]);
      expect(winners.map((r) => r.clearBindingCookie.includes("Max-Age=0"))).toEqual([true, true]);
      await expect(callback(a)).rejects.toThrow("unauthenticated");
      expect(exchanges).toBe(2);
    } finally {
      vi.unstubAllGlobals();
      await pool.end();
    }
  },
);
