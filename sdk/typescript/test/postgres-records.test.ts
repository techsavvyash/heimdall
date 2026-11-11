import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { RecordCipher } from "../src/internal/envelopes.js";
import { PostgresRecords } from "../src/internal/postgres/records.js";
const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/postgres-records.json", import.meta.url),
    "utf8",
  ),
);
const cipher = new RecordCipher(fixture.keyId, {
  [fixture.keyId]: Buffer.alloc(32, fixture.keyByte),
});
test("rejects malformed storage scopes before connection", () => {
  const pool = new Pool();
  expect(() => new PostgresRecords(pool, cipher, "../bad", "test")).toThrow("invalid_argument");
});
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "PostgreSQL encrypted records and language interoperability",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const store = new PostgresRecords(pool, cipher, fixture.productId, fixture.environment);
    const value = Buffer.from(fixture.value);
    try {
      await store.create("session", "from-ts", value, fixture.expiresAt);
      const first = await store.create("session", "ts-race", value, fixture.expiresAt);
      expect(first).not.toBeNull();
      await expect(store.create("session", "ts-race", value, fixture.expiresAt)).rejects.toThrow(
        "conflict",
      );
      const results = await Promise.all(
        Array.from({ length: 8 }, () =>
          store.compareAndSwap("session", "ts-race", first!.revision, value, fixture.expiresAt),
        ),
      );
      expect(results.filter(Boolean)).toHaveLength(1);
      expect(
        await new PostgresRecords(pool, cipher, "support", "test").get("session", "ts-race"),
      ).toBeNull();
      expect(await store.get("login", "ts-race")).toBeNull();
      expect(
        await new PostgresRecords(pool, cipher, "ledger", "test", () => fixture.expiresAt).get(
          "session",
          "ts-race",
        ),
      ).toBeNull();
      await store.delete("session", "ts-race");
      const recreated = await store.create("session", "ts-race", value, fixture.expiresAt);
      expect(recreated!.revision).not.toBe(first!.revision);
      await expect(store.get("session", "ts-race", AbortSignal.abort())).rejects.toThrow(
        "cancelled",
      );
      await pool.query(
        "UPDATE heimdall.records SET expires_at=expires_at+1 WHERE product_id='ledger' AND envelope=$1",
        [
          (
            await pool.query(
              "SELECT envelope FROM heimdall.records WHERE key_hash=sha256('ts-race'::bytea)",
            )
          ).rows[0].envelope,
        ],
      );
      await expect(store.get("session", "ts-race")).rejects.toThrow("unavailable");
      // A blocked dispatched write has an unknown outcome; do not infer rollback.
      const lock = await pool.connect();
      await lock.query("BEGIN");
      await lock.query("LOCK TABLE heimdall.records IN ACCESS EXCLUSIVE MODE");
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), 150);
      try {
        await expect(store.delete("session", "ts-race", controller.signal)).rejects.toThrow(
          "outcome_unknown",
        );
      } finally {
        clearTimeout(timer);
        await lock.query("ROLLBACK");
        lock.release();
      }
      expect((await store.get("session", "from-ts"))?.value).toEqual(value);
    } finally {
      await pool.end();
    }
  },
  15000,
);

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !process.env.HEIMDALL_TEST_READ_PEER)(
  "reads Go record",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    try {
      const store = new PostgresRecords(pool, cipher, fixture.productId, fixture.environment);
      expect((await store.get("session", "from-go"))?.value).toEqual(Buffer.from(fixture.value));
    } finally {
      await pool.end();
    }
  },
);
