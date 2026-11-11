import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { Pool } from "pg";
import { expect, test } from "vitest";
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
        store(fault, "identity-fault-ts").invalidateIdentity(
          fixture.record.issuer,
          fixture.record.subject,
          4000,
          "inactive",
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
