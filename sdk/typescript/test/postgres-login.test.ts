import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { RecordCipher } from "../src/internal/envelopes.js";
import { HeimdallError } from "../src/internal/errors.js";
import type { LoginScope } from "../src/internal/login-contract.js";
import { BrowserLoginRecords } from "../src/internal/postgres/login.js";
import { PostgresRecords } from "../src/internal/postgres/records.js";

const {
  target,
  lifetimeMs,
  federation,
}: {
  target: Omit<LoginScope, "browserBindingDigest">;
  lifetimeMs: number;
  federation: { intentId: string; authRequestId: string };
} = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/browser-login-records.json", import.meta.url),
    "utf8",
  ),
);

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "browser callback claims require the exact policy and browser binding and allow one winner",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const cipher = new RecordCipher("k1", { k1: Buffer.alloc(32, 7) });
    let now = Date.now();
    const clock = () => now;
    const store = new PostgresRecords(
      pool,
      pool,
      cipher,
      target.productId,
      target.environment,
      clock,
    );
    const login = new BrowserLoginRecords(store, clock);
    try {
      await expect(login.start({ ...target, productId: "support" })).rejects.toThrow(
        "invalid_argument",
      );
      const started = await login.start(target);
      const second = await login.start(target);
      expect(started.expiresAt - now).toBe(lifetimeMs);
      expect(
        new Set([started.state, started.browserBinding, started.nonce, started.pkceChallenge]).size,
      ).toBe(4);
      await expect(login.claim(target, started.state, second.browserBinding)).rejects.toThrow(
        "unauthenticated",
      );
      await expect(
        login.claim({ ...target, tenantPolicy: null }, started.state, started.browserBinding),
      ).rejects.toThrow("unauthenticated");
      await expect(
        login.claim({ ...target, productId: "support" }, started.state, started.browserBinding),
      ).rejects.toThrow("unauthenticated");
      await expect(
        login.claim(target, started.state, started.browserBinding, AbortSignal.abort()),
      ).rejects.toThrow("cancelled");
      const claims = await Promise.allSettled(
        Array.from({ length: 8 }, () => login.claim(target, started.state, started.browserBinding)),
      );
      const winners = claims.flatMap((claim) =>
        claim.status === "fulfilled" ? [claim.value] : [],
      );
      expect(winners).toHaveLength(1);
      expect(winners[0]?.nonce).toBe(started.nonce);
      expect(createHash("sha256").update(winners[0]!.pkceVerifier).digest("base64url")).toBe(
        started.pkceChallenge,
      );
      expect(
        claims.flatMap((claim) => (claim.status === "rejected" ? [claim.reason.message] : [])),
      ).toEqual(Array(7).fill("unauthenticated"));
      const authorizationUrl = "https://idp.example.test/authorize";
      const federated = await login.startFederation(target, async () => ({
        ...federation,
        authorizationUrl,
      }));
      expect(federated.authorizationUrl).toBe(authorizationUrl);
      await expect(login.claim(target, federated.state, federated.browserBinding)).rejects.toThrow(
        "unauthenticated",
      );
      await expect(
        login.claimFederation(target, federated.state, federated.browserBinding, "wrong_intent"),
      ).rejects.toThrow("unauthenticated");
      await expect(
        login.claimFederation(
          { ...target, tenantPolicy: { ...target.tenantPolicy!, policyVersion: "v2" } },
          federated.state,
          federated.browserBinding,
          federation.intentId,
        ),
      ).rejects.toThrow("unauthenticated");
      const federatedClaims = await Promise.allSettled(
        Array.from({ length: 8 }, () =>
          login.claimFederation(
            target,
            federated.state,
            federated.browserBinding,
            federation.intentId,
          ),
        ),
      );
      expect(
        federatedClaims.flatMap((claim) => (claim.status === "fulfilled" ? [claim.value] : [])),
      ).toEqual([
        {
          nonce: federated.nonce,
          pkceVerifier: expect.any(String),
          authRequestId: federation.authRequestId,
          expiresAt: federated.expiresAt,
        },
      ]);
      await expect(
        login.claimFederation(target, second.state, second.browserBinding, federation.intentId),
      ).rejects.toThrow("unauthenticated");
      const previousSessionKey = started.browserBinding;
      const bound = await login.startFederation(target, async () => ({
        ...federation,
        authorizationUrl,
        previousSessionKey,
      }));
      for (const wrong of [undefined, second.browserBinding]) {
        await expect(
          login.claimFederation(
            target,
            bound.state,
            bound.browserBinding,
            federation.intentId,
            undefined,
            wrong,
          ),
        ).rejects.toThrow("unauthenticated");
      }
      expect(
        (
          await login.claimFederation(
            target,
            bound.state,
            bound.browserBinding,
            federation.intentId,
            undefined,
            previousSessionKey,
          )
        ).nonce,
      ).toBe(bound.nonce);
      const unknown = new BrowserLoginRecords(
        {
          matchesScope: store.matchesScope.bind(store),
          create: store.create.bind(store),
          get: store.get.bind(store),
          compareAndSwap: async (...args) => {
            await store.compareAndSwap(...args);
            throw new HeimdallError("outcome_unknown");
          },
        },
        clock,
      );
      const lost = await login.start(target);
      await expect(unknown.claim(target, lost.state, lost.browserBinding)).rejects.toThrow(
        "outcome_unknown",
      );
      await expect(login.claim(target, lost.state, lost.browserBinding)).rejects.toThrow(
        "unauthenticated",
      );

      const lostFederation = await login.startFederation(target, async () => ({
        ...federation,
        authorizationUrl,
      }));
      await expect(
        unknown.claimFederation(
          target,
          lostFederation.state,
          lostFederation.browserBinding,
          federation.intentId,
        ),
      ).rejects.toThrow("outcome_unknown");
      await expect(
        login.claimFederation(
          target,
          lostFederation.state,
          lostFederation.browserBinding,
          federation.intentId,
        ),
      ).rejects.toThrow("unauthenticated");

      const malformed = await login.start(target);
      const stored = (await store.get("login", malformed.state))!;
      const payload = JSON.parse(stored.value.toString());
      expect(payload.browserBindingDigest).toBe(
        createHash("sha256").update(malformed.browserBinding).digest("hex"),
      );
      await store.compareAndSwap(
        "login",
        malformed.state,
        stored.revision,
        Buffer.from(JSON.stringify({ ...payload, nonce: "" })),
        stored.expiresAt,
      );
      await expect(login.claim(target, malformed.state, malformed.browserBinding)).rejects.toThrow(
        "unavailable",
      );
      now = second.expiresAt;
      await expect(login.claim(target, second.state, second.browserBinding)).rejects.toThrow(
        "unauthenticated",
      );
    } finally {
      await pool.end();
    }
  },
);
