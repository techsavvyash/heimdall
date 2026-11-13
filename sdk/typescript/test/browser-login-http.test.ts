import { HeimdallError } from "../src/internal/errors.js";
import { AuditedAuthentication } from "../src/internal/audited-authentication.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { browserSession } from "../src/internal/browser-session.js";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { SessionRecords } from "../src/internal/postgres/sessions.js";
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
    let refreshes = 0;
    let duringRefresh = async () => {};
    const exp = Math.floor(Date.now() / 1000) + 300;
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      if (url.endsWith("/.well-known/openid-configuration")) return Response.json(metadata);
      if (url === metadata.jwks_uri) return Response.json(jwks);
      if (url === metadata.introspection_endpoint)
        return Response.json({ active: true, iss: issuer, sub: "alice", aud: [f.audience], exp });
      if (url === issuer + "/v2/users/alice")
        return Response.json({ user: { userId: "alice", state: "USER_STATE_ACTIVE", human: {} } });
      expect(url).toBe(metadata.token_endpoint);
      const body = init!.body as URLSearchParams;
      const refreshing = body.get("grant_type") === "refresh_token";
      expect(refreshing ? body.get("refresh_token") : body.get("redirect_uri")).toBe(
        refreshing ? "original-refresh" : f.redirectUri,
      );
      if (refreshing) {
        refreshes++;
        await duringRefresh();
      } else exchanges++;
      let nonce;
      if (!refreshing) {
        const challenge = createHash("sha256")
          .update(body.get("code_verifier")!)
          .digest("base64url");
        nonce = pending.get(challenge);
      }
      expect(refreshing || Boolean(nonce)).toBe(true);
      const sign = (aud: string, claims: Record<string, unknown>) =>
        new SignJWT(claims)
          .setProtectedHeader({ alg: "RS256", kid: "login-http" })
          .setIssuer(issuer)
          .setSubject("alice")
          .setAudience(aud)
          .setIssuedAt()
          .setExpirationTime(exp)
          .sign(privateKey);
      return Response.json({
        token_type: "Bearer",
        access_token: await sign(f.audience, {}),
        ...(refreshing ? {} : { id_token: await sign(f.clientId, { nonce }) }),
        refresh_token: refreshing ? "replacement-refresh" : "original-refresh",
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
      const store = new SessionRecords(pool, pool, cipher, f.productId, f.environment, (value) =>
        JSON.parse(Buffer.from(value).toString()),
      );
      const audit = new PostgresAudit(
        pool,
        f.productId,
        f.environment,
        ["siem"],
        validateAuditShape,
      );
      let failAudit = true;
      const auth = new AuditedAuthentication(
        {
          issuer,
          profile: "production",
          jwksUri: metadata.jwks_uri,
          productId: f.productId,
          environment: f.environment,
          policyVersion: "p1",
          audience: f.audience,
          clientId: f.clientId,
        },
        async () => "fixture",
        store.identityGate(f.productId, f.environment),
        async (event, signal) => {
          if (failAudit) throw new Error();
          return audit.append(event, signal);
        },
      );
      const sessions = browserSession(http, auth, store, {
        returnTo: "/workspaces",
      });
      const requestFor = ({ state, cookie }: typeof a) =>
        new Request(`${f.redirectUri}?code=${f.code}&state=${state}`, { headers: { cookie } });
      const denied = await start();
      await expect(sessions.callback(requestFor(denied))).rejects.toThrow("audit_unavailable");
      expect(
        Number(
          (
            await pool.query(
              "SELECT count(*) FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind='session'",
              [f.productId, f.environment],
            )
          ).rows[0].count,
        ),
      ).toBe(0);
      failAudit = false;
      const uncertain = browserSession(
        http,
        auth,
        {
          getSession: store.getSession.bind(store),
          compareAndSwapSession: store.compareAndSwapSession.bind(store),
          deleteSession: store.deleteSession.bind(store),
          createSession: async () => {
            throw new HeimdallError("outcome_unknown");
          },
        },
        {
          returnTo: "/workspaces",
        },
      );
      await expect(uncertain.callback(requestFor(await start()))).rejects.toThrow(
        "outcome_unknown",
      );
      const response = await sessions.callback(requestFor(await start()));
      const cookies = response.headers.getSetCookie();
      expect(response.status).toBe(303);
      expect(response.headers.get("location")).toBe("/workspaces");
      expect(cookies).toHaveLength(2);
      expect(cookies[0]).toContain("Max-Age=0");
      expect(cookies[1]).toMatch(/^__Host-heimdall-.*-session=[A-Za-z0-9_-]{43};/);
      const opaque = cookies[1]!.split(";")[0]!.split("=")[1]!;
      const key = createHash("sha256").update(opaque).digest("base64url");
      expect(await store.getSession(opaque)).toBeNull();
      const stored = (await store.getSession(key))!;
      expect(JSON.parse(stored.value.toString())).toMatchObject({
        productId: f.productId,
        environment: f.environment,
        subject: "alice",
        state: "active",
        policyVersion: "p1",
      });
      expect(
        Number(
          (
            await pool.query(
              "SELECT count(*) FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2",
              [f.productId, f.environment],
            )
          ).rows[0].count,
        ),
      ).toBe(2);
      const sessionCookie = cookies[1]!.split(";")[0]!;
      const protectedUrl = new URL("/workspaces", f.redirectUri);
      const authenticated = new Request(protectedUrl, { headers: { cookie: sessionCookie } });
      expect((await sessions.authenticate(authenticated)).subject).toBe("alice");
      const touched = (await store.getSession(key))!;
      expect(touched.revision).not.toBe(stored.revision);
      const logout = (origin: string) =>
        new Request(new URL("/auth/logout", f.redirectUri), {
          method: "POST",
          headers: { cookie: sessionCookie, origin },
        });
      await expect(sessions.logout(logout("https://other.example.test"))).rejects.toThrow(
        "forbidden",
      );
      expect(await store.getSession(key)).not.toBeNull();
      const race = browserSession(
        http,
        {
          verify: async (...args) => {
            const actor = await auth.verify(...args);
            const cleared = await sessions.logout(logout(protectedUrl.origin));
            expect(cleared.status).toBe(204);
            expect(cleared.headers.get("set-cookie")).toContain("Max-Age=0");
            return actor;
          },
        },
        store,
        { returnTo: "/workspaces" },
      );
      await expect(race.authenticate(authenticated)).rejects.toThrow("unauthenticated");
      expect(await store.getSession(key)).toBeNull();
      await expect(sessions.authenticate(authenticated)).rejects.toThrow("unauthenticated");
      expect((await sessions.logout(logout(protectedUrl.origin))).status).toBe(204);
      const expiredSession = async () => {
        const response = await sessions.callback(requestFor(await start()));
        const cookie = response.headers.getSetCookie()[1]!.split(";")[0]!;
        const key = createHash("sha256").update(cookie.split("=")[1]!).digest("base64url");
        const stored = (await store.getSession(key))!;
        const payload = JSON.parse(stored.value.toString());
        payload.accessTokenExpiresAt = Date.now() - 1;
        await store.compareAndSwapSession(
          key,
          stored.revision,
          Buffer.from(JSON.stringify(payload)),
          stored.expiresAt,
        );
        return {
          key,
          request: new Request(protectedUrl, { headers: { cookie } }),
          logout: new Request(new URL("/auth/logout", protectedUrl), {
            method: "POST",
            headers: { cookie, origin: protectedUrl.origin },
          }),
        };
      };
      const rotated = await expiredSession();
      duringRefresh = async () => {
        await expect(sessions.authenticate(rotated.request)).rejects.toThrow("unavailable");
      };
      expect((await sessions.authenticate(rotated.request)).subject).toBe("alice");
      expect(JSON.parse((await store.getSession(rotated.key))!.value.toString())).toMatchObject({
        state: "active",
        refreshToken: "replacement-refresh",
      });
      expect(refreshes).toBe(1);

      const removed = await expiredSession();
      duringRefresh = async () => {
        await sessions.logout(removed.logout);
      };
      await expect(sessions.authenticate(removed.request)).rejects.toThrow("unauthenticated");
      expect(await store.getSession(removed.key)).toBeNull();
      expect(refreshes).toBe(2);

      const lost = await expiredSession();
      duringRefresh = async () => {
        throw new Error("lost");
      };
      await expect(sessions.authenticate(lost.request)).rejects.toThrow("unavailable");
      const later = browserSession(
        http,
        auth,
        store,
        { returnTo: "/workspaces" },
        () => Date.now() + 16_000,
      );
      await expect(later.authenticate(lost.request)).rejects.toThrow("unauthenticated");
      expect(refreshes).toBe(3);
    } finally {
      vi.unstubAllGlobals();
      await pool.end();
    }
  },
);
