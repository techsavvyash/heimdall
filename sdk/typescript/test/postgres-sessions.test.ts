import { AuditedAuthentication } from "../src/internal/audited-authentication.js";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { sessionIdentityContext } from "../src/internal/identity-context.js";
import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { Pool } from "pg";
import { expect, test, vi } from "vitest";
import { RecordCipher } from "../src/internal/envelopes.js";
import { HeimdallError } from "../src/internal/errors.js";
import { SessionRecords } from "../src/internal/postgres/sessions.js";
import { PostgresRecords } from "../src/internal/postgres/records.js";
const root = new URL("../../../contracts/", import.meta.url);
const read = (path: string) => JSON.parse(readFileSync(new URL(path, root), "utf8"));
const fixture = read("fixtures/session-storage.json");
const ajv = new Ajv2020({ strict: true });
ajv.addSchema(read("enterprise-evidence.schema.json"));
const shape = ajv.compile(read("session-payload.schema.json"));
const cipher = new RecordCipher("test-key", { "test-key": Buffer.alloc(32, 7) });
const validate = (raw: Uint8Array) => {
  const value = JSON.parse(Buffer.from(raw).toString("utf8"));
  if (!shape(value)) throw new HeimdallError("invalid_argument");
  return value as { productId: string; environment: string; issuer: string; subject: string };
};
const payload = (
  productId: string,
  subject = fixture.record.subject,
  extra: Record<string, unknown> = {},
) => Buffer.from(JSON.stringify({ ...fixture.record, productId, subject, ...extra }));
const store = (pool: Pool, product = "ledger-identity", environment = "production") =>
  new SessionRecords(pool, cipher, product, environment, validate, () => fixture.now);
test("bound session validates before database access", async () => {
  const pool = new Pool();
  try {
    await expect(
      store(pool).createSession("bad", Buffer.from("{}"), fixture.expiresAt),
    ).rejects.toThrow("invalid_argument");
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "identity-bound sessions and atomic invalidation",
  async () => {
    const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: "-c role=heimdall_identity_runtime",
    });
    const sessions = store(pool, "identity-ts");
    try {
      expect(await sessions.isIdentityDenied(fixture.record.issuer, fixture.record.subject)).toBe(
        false,
      );
      const first = await sessions.createSession(
        "ts-one",
        payload("identity-ts"),
        fixture.expiresAt,
      );
      await sessions.createSession("ts-two", payload("identity-ts"), fixture.expiresAt);
      await sessions.createSession(
        "ts-other",
        payload("identity-ts", "someone-else"),
        fixture.expiresAt,
      );
      await sessions.createSession(
        "ts-other-issuer",
        payload("identity-ts", fixture.record.subject, { issuer: "https://other.example.com" }),
        fixture.expiresAt,
      );
      const generic = new PostgresRecords(
        pool,
        cipher,
        "identity-ts",
        "production",
        () => fixture.now,
      );
      expect(await generic.get("session", "ts-one")).toBeNull();
      expect(
        await generic.compareAndSwap(
          "session",
          "ts-one",
          first!.revision,
          Buffer.from("unbound"),
          fixture.expiresAt,
        ),
      ).toBeNull();
      await generic.delete("session", "ts-one");
      expect(await sessions.getSession("ts-one")).not.toBeNull();
      expect(
        await sessions.compareAndSwapSession(
          "ts-one",
          first!.revision,
          payload("identity-ts", "someone-else"),
          fixture.expiresAt,
        ),
      ).toBeNull();
      expect(
        await sessions.compareAndSwapSession(
          "ts-one",
          first!.revision,
          payload("identity-ts", fixture.record.subject, { lastActivityAt: 2100 }),
          fixture.expiresAt,
        ),
      ).not.toBeNull();
      await admin.query(
        "UPDATE heimdall.records SET identity_hash=decode(repeat('01',32),'hex') WHERE key_hash=sha256('ts-one'::bytea) AND product_id='identity-ts'",
      );
      await expect(sessions.getSession("ts-one")).rejects.toThrow("unavailable");
      await admin.query(
        "UPDATE heimdall.records SET identity_hash=decode($1,'hex') WHERE key_hash=sha256('ts-one'::bytea) AND product_id='identity-ts'",
        [fixture.identityHash],
      );
      expect(
        await sessions.invalidateIdentity(
          fixture.record.issuer,
          fixture.record.subject,
          5000,
          "inactive",
        ),
      ).toBe(2);
      expect(await sessions.isIdentityDenied(fixture.record.issuer, fixture.record.subject)).toBe(
        true,
      );
      expect(await sessions.isIdentityDenied(fixture.record.issuer, "someone-else")).toBe(false);
      expect(await sessions.getSession("ts-one")).toBeNull();
      expect(await sessions.getSession("ts-two")).toBeNull();
      expect(await sessions.getSession("ts-other")).not.toBeNull();
      expect(await sessions.getSession("ts-other-issuer")).not.toBeNull();
      await expect(
        sessions.createSession("ts-resurrect", payload("identity-ts"), fixture.expiresAt),
      ).rejects.toThrow("unauthenticated");
      await expect(
        sessions.compareAndSwapSession(
          "ts-one",
          first!.revision,
          payload("identity-ts"),
          fixture.expiresAt,
        ),
      ).rejects.toThrow("unauthenticated");
      await sessions.invalidateIdentity(
        fixture.record.issuer,
        fixture.record.subject,
        4000,
        "deleted",
      );
      await sessions.invalidateIdentity(
        fixture.record.issuer,
        fixture.record.subject,
        6000,
        "inactive",
      );
      expect(
        (
          await admin.query(
            "SELECT observed_at::text,reason FROM heimdall.identity_denials WHERE product_id='identity-ts' AND identity_hash=decode($1,'hex')",
            [fixture.identityHash],
          )
        ).rows[0],
      ).toEqual({ observed_at: "6000", reason: "deleted" });
      await expect(
        sessions.invalidateIdentity(
          fixture.record.issuer,
          fixture.record.subject,
          7000,
          "active" as "inactive",
        ),
      ).rejects.toThrow("invalid_argument");
      await store(pool, "identity-other-ts").createSession(
        "ts-isolated",
        payload("identity-other-ts"),
        fixture.expiresAt,
      );
      await store(pool, "identity-ts", "staging").createSession(
        "ts-stage",
        payload("identity-ts", fixture.record.subject, { environment: "staging" }),
        fixture.expiresAt,
      );
      const raced = await Promise.allSettled([
        ...Array.from({ length: 8 }, (_, i) =>
          sessions.createSession(
            `ts-race-session-${i}`,
            payload("identity-ts", "race"),
            fixture.expiresAt,
          ),
        ),
        sessions.invalidateIdentity(fixture.record.issuer, "race", 5000, "inactive"),
      ]);
      expect(
        raced.every(
          (result) =>
            result.status === "fulfilled" ||
            (result.reason instanceof HeimdallError && result.reason.code === "unauthenticated"),
        ),
      ).toBe(true);
      expect(
        (
          await admin.query(
            "SELECT count(*)::int AS n FROM heimdall.records WHERE product_id='identity-ts' AND key_hash IN (SELECT sha256(('ts-race-session-'||i)::bytea) FROM generate_series(0,7) i)",
          )
        ).rows[0].n,
      ).toBe(0);
      await sessions.createSession(
        "ts-rollback",
        payload("identity-ts", "rollback"),
        fixture.expiresAt,
      );
      await admin.query(
        "CREATE FUNCTION heimdall.ts_deny_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END; $$; CREATE TRIGGER ts_deny_failure BEFORE DELETE ON heimdall.records FOR EACH ROW WHEN(OLD.key_hash=sha256('ts-rollback'::bytea)) EXECUTE FUNCTION heimdall.ts_deny_failure()",
      );
      try {
        await expect(
          sessions.invalidateIdentity(fixture.record.issuer, "rollback", 5000, "inactive"),
        ).rejects.toThrow("unavailable");
        expect(await sessions.getSession("ts-rollback")).not.toBeNull();
      } finally {
        await admin.query(
          "DROP TRIGGER ts_deny_failure ON heimdall.records; DROP FUNCTION heimdall.ts_deny_failure()",
        );
      }
      const shared = store(pool);
      await shared.createSession("from-ts-session", payload("ledger-identity"), fixture.expiresAt);
      await shared.createSession(
        "cross-invalidation",
        payload("ledger-identity", "cross-deny"),
        fixture.expiresAt,
      );
      const lock = await admin.connect();
      await lock.query("BEGIN");
      await lock.query("SELECT pg_advisory_xact_lock($1::bigint)", [fixture.lock]);
      try {
        await expect(
          shared.createSession(
            "locked-ts",
            payload("ledger-identity"),
            fixture.expiresAt,
            AbortSignal.timeout(150),
          ),
        ).rejects.toThrow("deadline_exceeded");
      } finally {
        await lock.query("ROLLBACK");
        lock.release();
      }
    } finally {
      await pool.end();
      await admin.end();
    }
  },
  20000,
);
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !process.env.HEIMDALL_TEST_READ_PEER)(
  "bound session cross-language read and denial",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const shared = store(pool);
    try {
      expect(JSON.parse((await shared.getSession("from-go-session"))!.value.toString())).toEqual(
        fixture.record,
      );
      expect(await shared.getSession("cross-invalidation")).toBeNull();
      await expect(
        shared.createSession(
          "cross-resurrect",
          payload("ledger-identity", "cross-deny"),
          fixture.expiresAt,
        ),
      ).rejects.toThrow("unauthenticated");
    } finally {
      await pool.end();
    }
  },
);

test.skipIf(!process.env.HEIMDALL_TEST_IDENTITY_COMMIT_LOSS)(
  "identity invalidation committed response loss",
  async () => {
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const fault = new Pool({ connectionString: process.env.HEIMDALL_TEST_IDENTITY_COMMIT_LOSS });
    try {
      const healthy = store(direct, "identity-fault-ts");
      await healthy.createSession("fault-session", payload("identity-fault-ts"), fixture.expiresAt);
      await expect(
        store(fault, "identity-fault-ts")
          .identityGate("identity-fault-ts", "production")
          .invalidate(
            {
              issuer: fixture.record.issuer,
              subject: fixture.record.subject,
              context: "bearer",
              tokenDigest: "a".repeat(64),
              reason: "identity_inactive",
            },
            new AbortController().signal,
          ),
      ).rejects.toThrow("outcome_unknown");
      expect(await healthy.getSession("fault-session")).toBeNull();
      const state = await direct.query(
        "SELECT (SELECT count(*) FROM heimdall.identity_denials WHERE product_id='identity-fault-ts')::int AS denials,(SELECT count(*) FROM heimdall.records WHERE product_id='identity-fault-ts')::int AS sessions",
      );
      expect(state.rows[0]).toEqual({ denials: 1, sessions: 0 });
      expect(
        await healthy.invalidateIdentity(
          fixture.record.issuer,
          fixture.record.subject,
          4000,
          "inactive",
        ),
      ).toBe(0);
      await expect(
        healthy.createSession("retry-session", payload("identity-fault-ts"), fixture.expiresAt),
      ).rejects.toThrow("unauthenticated");
    } finally {
      await fault.end();
      await direct.end();
    }
  },
);

test("conditional session invalidation rejects malformed revisions before access", async () => {
  const pool = new Pool();
  try {
    for (const revision of [
      ...read("fixtures/session-invalidation.json").invalidRevisions,
      1,
      null,
    ])
      await expect(
        store(pool).invalidateSessionVersion(
          "key",
          revision,
          fixture.record.issuer,
          fixture.record.subject,
        ),
      ).rejects.toThrow("invalid_argument");
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "conditional session invalidation preserves replacements",
  async () => {
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: "-c role=heimdall_identity_runtime",
    });
    const s = store(pool, "conditional-ts");
    const f = read("fixtures/session-invalidation.json");
    const invalidate = (key: string, revision: string) =>
      s.invalidateSessionVersion(key, revision, fixture.record.issuer, fixture.record.subject);
    try {
      const first = (await s.createSession("one", payload("conditional-ts"), fixture.expiresAt))!;
      expect(
        await s.invalidateSessionVersion("one", first.revision, fixture.record.issuer, "other"),
      ).toBe(false);
      expect(
        await s.invalidateSessionVersion(
          "one",
          first.revision,
          "https://other.example.com",
          fixture.record.subject,
        ),
      ).toBe(false);
      expect(
        await store(pool, "conditional-other").invalidateSessionVersion(
          "one",
          first.revision,
          fixture.record.issuer,
          fixture.record.subject,
        ),
      ).toBe(false);
      expect(
        await store(pool, "conditional-ts", "staging").invalidateSessionVersion(
          "one",
          first.revision,
          fixture.record.issuer,
          fixture.record.subject,
        ),
      ).toBe(false);
      const newer = (await s.compareAndSwapSession(
        "one",
        first.revision,
        payload("conditional-ts"),
        fixture.expiresAt,
      ))!;
      expect(await invalidate("one", first.revision)).toBe(false);
      expect((await s.getSession("one"))!.revision).toBe(newer.revision);
      expect(await invalidate("one", newer.revision)).toBe(true);
      expect(await invalidate("one", newer.revision)).toBe(false);
      expect(
        await s.compareAndSwapSession(
          "one",
          newer.revision,
          payload("conditional-ts"),
          fixture.expiresAt,
        ),
      ).toBeNull();
      const replacement = (await s.createSession(
        "one",
        payload("conditional-ts"),
        fixture.expiresAt,
      ))!;
      expect(await invalidate("one", newer.revision)).toBe(false);
      expect((await s.getSession("one"))!.revision).toBe(replacement.revision);
      for (let i = 0; i < f.raceCount; i++) {
        const key = `race-${i}`;
        const record = (await s.createSession(key, payload("conditional-ts"), fixture.expiresAt))!;
        const [changed, removed] = await Promise.all([
          s.compareAndSwapSession(
            key,
            record.revision,
            payload("conditional-ts"),
            fixture.expiresAt,
          ),
          invalidate(key, record.revision),
        ]);
        expect(Number(changed !== null) + Number(removed)).toBe(1);
        expect((await s.getSession(key))?.revision ?? null).toBe(changed?.revision ?? null);
      }
      await store(pool).createSession(f.sharedKey, payload("ledger-identity"), fixture.expiresAt);
    } finally {
      await pool.end();
    }
  },
  20000,
);
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !process.env.HEIMDALL_TEST_READ_PEER)(
  "Go invalidates the TS observed session version",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    try {
      expect(
        await store(pool).getSession(read("fixtures/session-invalidation.json").sharedKey),
      ).toBeNull();
    } finally {
      await pool.end();
    }
  },
);
test.skipIf(!process.env.HEIMDALL_TEST_SESSION_DELETE_LOSS)(
  "conditional session deletion committed response loss",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const fault = new Pool({ connectionString: process.env.HEIMDALL_TEST_SESSION_DELETE_LOSS });
    const s = store(pool, "conditional-fault-ts");
    try {
      const record = (await s.createSession(
        "fault",
        payload("conditional-fault-ts"),
        fixture.expiresAt,
      ))!;
      await expect(
        store(fault, "conditional-fault-ts")
          .identityGate("conditional-fault-ts", "production")
          .invalidate(
            {
              issuer: fixture.record.issuer,
              subject: fixture.record.subject,
              context: sessionIdentityContext("fault", record.revision),
              tokenDigest: "a".repeat(64),
              reason: "token_inactive",
            },
            new AbortController().signal,
          ),
      ).rejects.toThrow("outcome_unknown");
      expect(await s.getSession("fault")).toBeNull();
      const replacement = (await s.createSession(
        "fault",
        payload("conditional-fault-ts"),
        fixture.expiresAt,
      ))!;
      expect(
        await s.invalidateSessionVersion(
          "fault",
          record.revision,
          fixture.record.issuer,
          fixture.record.subject,
        ),
      ).toBe(false);
      expect((await s.getSession("fault"))!.revision).toBe(replacement.revision);
    } finally {
      await fault.end();
      await pool.end();
    }
  },
);

test("identity gate refuses mismatched scope and malformed contexts before storage", async () => {
  const pool = new Pool();
  const sessions = store(pool);
  try {
    expect(() => sessions.identityGate("support", "production")).toThrow("invalid_argument");
    expect(() => sessions.identityGate("ledger-identity", "staging")).toThrow("invalid_argument");
    const gate = sessions.identityGate("ledger-identity", "production");
    const negative = {
      issuer: fixture.record.issuer,
      subject: fixture.record.subject,
      context: "session:key:0",
      tokenDigest: "a".repeat(64),
      reason: "token_inactive" as const,
    };
    await expect(gate.invalidate(negative, new AbortController().signal)).rejects.toThrow(
      "invalid_argument",
    );
    await expect(
      gate.invalidate({ ...negative, context: "bearer" }, AbortSignal.abort()),
    ).rejects.toThrow("cancelled");
    await expect(
      gate.invalidate({ ...negative, context: "bearer" }, new AbortController().signal),
    ).resolves.toBeUndefined();
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "identity verifier uses durable session gate",
  async () => {
    const f = read("fixtures/token-verification.json");
    const raw = f.vectors.find((v: { name: string }) => v.name === "valid").token;
    const cases = read("fixtures/identity-status.json");
    let current = cases.find((c: { name: string }) => c.name === "active-human");
    const inactive = cases.find((c: { name: string }) => c.name === "inactive-token");
    const identityInactive = cases.find(
      (c: { negativeReason?: string }) => c.negativeReason === "identity_inactive",
    );
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: "-c role=heimdall_identity_runtime",
    });
    const sessions = new SessionRecords(
      pool,
      cipher,
      "gate-ts",
      "production",
      validate,
      () => f.now,
    );
    let online = 0;
    let replacement: (() => Promise<void>) | undefined;
    vi.stubGlobal("fetch", async (input: unknown) => {
      const url = String(input);
      if (url.endsWith("/keys")) return Response.json(f.jwks);
      if (url.endsWith("/introspect")) {
        online++;
        if (replacement) {
          const fn = replacement;
          replacement = undefined;
          await fn();
        }
        return Response.json(current.introspection, { status: current.introspectionStatus });
      }
      return Response.json(current.user, { status: current.userStatus });
    });
    const gate = sessions.identityGate("gate-ts", "production");
    const config = {
      issuer: f.issuer,
      audience: f.audience,
      jwksUri: f.issuer + "/keys",
      profile: "production" as const,
      productId: "gate-ts",
      environment: "production",
      policyVersion: "p1",
      clientId: "client",
    };
    const auditPool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: "-c role=heimdall_audit_runtime",
    });
    const auditShape = ajv.compile(read("audit-event.schema.json"));
    const validateAudit = (event: Record<string, unknown>) => {
      if (!auditShape(event)) throw new HeimdallError("invalid_argument");
    };
    const audit = new PostgresAudit(auditPool, "gate-ts", "production", ["siem"], validateAudit);
    let committed = 0;
    const append = async (event: Record<string, unknown>, signal: AbortSignal) => {
      const receipt = await audit.append(event, signal);
      committed++;
      return receipt;
    };
    const fresh = new AuditedAuthentication(
      { ...config, freshnessMs: 0 },
      async () => "secret",
      gate,
      append,
      validateAudit,
      () => f.now,
    );
    const warm = new AuditedAuthentication(
      config,
      async () => "secret",
      gate,
      append,
      validateAudit,
      () => f.now,
    );
    const value = payload("gate-ts", f.subject, {
      issuer: f.issuer,
      accessToken: raw,
      createdAt: f.now - 1000,
      lastActivityAt: f.now,
      identityValidatedAt: f.now,
      absoluteExpiresAt: f.now + 28800000,
      idleExpiresAt: f.now + 1800000,
      accessTokenExpiresAt: f.now + 3600000,
    });
    const expires = f.now + 1800000;
    try {
      const first = await sessions.createSession("first", value, expires);
      const other = await sessions.createSession("other", value, expires);
      if (!first || !other) throw new Error("session creation failed");
      const firstContext = sessionIdentityContext("first", first.revision);
      expect((await warm.verify(raw, firstContext)).subject).toBe(f.subject);
      current = inactive;
      await expect(fresh.verify(raw, "bearer")).rejects.toThrow("unauthenticated");
      expect(await sessions.getSession("first")).not.toBeNull();
      await expect(fresh.verify(raw, firstContext)).rejects.toThrow("unauthenticated");
      expect(await sessions.getSession("first")).toBeNull();
      expect(await sessions.getSession("other")).not.toBeNull();
      expect(await sessions.isIdentityDenied(f.issuer, f.subject)).toBe(false);
      replacement = async () => {
        await sessions.compareAndSwapSession("other", other.revision, value, expires);
      };
      await expect(
        fresh.verify(raw, sessionIdentityContext("other", other.revision)),
      ).rejects.toThrow("unauthenticated");
      const replaced = await sessions.getSession("other");
      expect(replaced?.revision).not.toBe(other.revision);
      expect(replaced).not.toBeNull();
      const untouched = payload("gate-ts", "unrelated", { issuer: f.issuer });
      await sessions.createSession("unrelated", untouched, expires);
      current = identityInactive;
      await expect(
        fresh.verify(raw, sessionIdentityContext("other", replaced!.revision)),
      ).rejects.toThrow("unauthenticated");
      expect(await sessions.isIdentityDenied(f.issuer, f.subject)).toBe(true);
      expect(await sessions.getSession("other")).toBeNull();
      expect(await sessions.getSession("unrelated")).not.toBeNull();
      await gate.invalidate(
        {
          issuer: f.issuer,
          subject: f.subject,
          context: "bearer",
          tokenDigest: "a".repeat(64),
          reason: "identity_deleted",
        },
        new AbortController().signal,
      );
      expect(
        (
          await pool.query(
            "SELECT reason FROM heimdall.identity_denials WHERE product_id='gate-ts'",
          )
        ).rows[0].reason,
      ).toBe("deleted");
      const before = online;
      current = cases[0];
      await expect(warm.verify(raw, firstContext)).rejects.toThrow("unauthenticated");
      expect(online).toBe(before);
      await expect(sessions.createSession("resurrect", value, expires)).rejects.toThrow(
        "unauthenticated",
      );
      expect(committed).toBe(6);
      expect(
        (
          await auditPool.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_events WHERE product_id='gate-ts' AND environment='production'",
          )
        ).rows[0].n,
      ).toBe(6);
      expect(
        (
          await auditPool.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE product_id='gate-ts' AND environment='production'",
          )
        ).rows[0].n,
      ).toBe(6);
    } finally {
      await auditPool.end();
      vi.unstubAllGlobals();
      await pool.end();
    }
  },
);
