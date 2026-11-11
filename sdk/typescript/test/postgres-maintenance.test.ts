import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { RecordMaintenance } from "../src/internal/postgres/maintenance.js";
const fixture = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/record-cleanup.json", import.meta.url), "utf8"),
);
const url = process.env.HEIMDALL_TEST_DATABASE_URL;
const seed = async (
  pool: Pool,
  product: string,
  key: string,
  expiry: number,
  environment = "test",
  kind = "session",
  identity = false,
) => {
  await pool.query(
    "INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at,identity_hash) VALUES($1,$2,$3,sha256($4::bytea),'retired','unreadable',$5,CASE WHEN $6 THEN sha256('identity'::bytea) ELSE NULL END)",
    [product, environment, kind, key, expiry, identity],
  );
};
test("record cleanup validates bounds and cancellation without connecting", async () => {
  const pool = new Pool();
  try {
    expect(() => new RecordMaintenance(pool, "../bad", "test")).toThrow("invalid_argument");
    expect(() => new RecordMaintenance(pool, "cleanup\n", "test")).toThrow("invalid_argument");
    for (const limit of [...fixture.invalidLimits, 1.5, NaN])
      await expect(
        new RecordMaintenance(pool, "cleanup", "test", () => fixture.cutoff).cleanupExpired(limit),
      ).rejects.toThrow("invalid_argument");
    for (const now of [-1, NaN, Number.MAX_SAFE_INTEGER + 1])
      await expect(
        new RecordMaintenance(pool, "cleanup", "test", () => now).cleanupExpired(1),
      ).rejects.toThrow("invalid_argument");
    await expect(
      new RecordMaintenance(pool, "cleanup", "test").cleanupExpired(1, AbortSignal.abort()),
    ).rejects.toThrow("cancelled");
  } finally {
    await pool.end();
  }
});
test.skipIf(!url || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "bounded cleanup preserves active records and skips refresh locks",
  async () => {
    const pool = new Pool({ connectionString: url });
    const product = "cleanup-ts";
    const m = new RecordMaintenance(pool, product, "test", () => fixture.cutoff);
    try {
      for (const r of fixture.records)
        await seed(pool, product, r.key, r.expiresAt, r.environment, r.kind, r.identity);
      await seed(pool, "cleanup-foreign-ts", "foreign", 0);
      await pool.query(
        "INSERT INTO heimdall.identity_denials VALUES($1,'test',sha256('identity'::bytea),0,'deleted')",
        [product],
      );
      expect(await m.cleanupExpired(1)).toBe(1);
      expect(await m.cleanupExpired(100)).toBe(1);
      expect(await m.cleanupExpired(100)).toBe(0);
      const rows = await pool.query(
        "SELECT encode(key_hash,'hex') AS key FROM heimdall.records WHERE product_id=$1",
        [product],
      );
      expect(rows.rowCount).toBe(fixture.remaining.length);
      for (const key of fixture.remaining)
        expect(
          (
            await pool.query(
              "SELECT count(*)::int AS n FROM heimdall.records WHERE product_id=$1 AND key_hash=sha256($2::bytea)",
              [product, key],
            )
          ).rows[0].n,
        ).toBe(1);
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS n FROM heimdall.identity_denials WHERE product_id=$1",
            [product],
          )
        ).rows[0].n,
      ).toBe(1);
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS n FROM heimdall.records WHERE product_id='cleanup-foreign-ts'",
          )
        ).rows[0].n,
      ).toBe(1);
      await seed(pool, product, "locked", 999);
      const lock = await pool.connect();
      try {
        await lock.query("BEGIN");
        await lock.query(
          "UPDATE heimdall.records SET expires_at=2000 WHERE product_id=$1 AND key_hash=sha256('locked'::bytea)",
          [product],
        );
        expect(await m.cleanupExpired(100)).toBe(0);
        await lock.query("COMMIT");
      } finally {
        await lock.query("ROLLBACK");
        lock.release();
      }
      expect(await m.cleanupExpired(100)).toBe(0);
      for (let i = 0; i < 101; i++) await seed(pool, product, `batch-${i}`, 0);
      const counts = await Promise.all(Array.from({ length: 8 }, () => m.cleanupExpired(20)));
      expect(counts.every((n) => n <= 20)).toBe(true);
      expect(counts.reduce((a, b) => a + b, 0)).toBe(101);
      expect(await m.cleanupExpired(100)).toBe(0);
      await seed(pool, product, "timeout", 0);
      const blocker = await pool.connect();
      try {
        await blocker.query("BEGIN");
        await blocker.query("LOCK TABLE heimdall.records IN ACCESS EXCLUSIVE MODE");
        await expect(m.cleanupExpired(1, AbortSignal.timeout(100))).rejects.toThrow(
          "deadline_exceeded",
        );
      } finally {
        await blocker.query("ROLLBACK");
        blocker.release();
      }
      expect(await m.cleanupExpired(100)).toBe(1);
      const readOnly = new Pool({
        connectionString: url,
        options: "-c default_transaction_read_only=on",
      });
      const restricted = new Pool({
        connectionString: url,
        options: "-c role=heimdall_identity_runtime",
      });
      try {
        await expect(
          new RecordMaintenance(readOnly, product, "test", () => fixture.cutoff).cleanupExpired(1),
        ).rejects.toThrow("unavailable");
        await seed(pool, product, "restricted", 0);
        expect(
          await new RecordMaintenance(
            restricted,
            product,
            "test",
            () => fixture.cutoff,
          ).cleanupExpired(1),
        ).toBe(1);
      } finally {
        await readOnly.end();
        await restricted.end();
      }
      await seed(pool, "cleanup-peer-ts", "from-ts", 0);
    } finally {
      await pool.end();
    }
  },
  15000,
);
test.skipIf(!url || !process.env.HEIMDALL_TEST_READ_PEER)(
  "cleanup removes Go expired records without their cipher",
  async () => {
    const pool = new Pool({ connectionString: url });
    try {
      expect(
        await new RecordMaintenance(pool, "cleanup-peer-go", "test", () => 1000).cleanupExpired(
          100,
        ),
      ).toBe(1);
    } finally {
      await pool.end();
    }
  },
);
test.skipIf(!url || !process.env.HEIMDALL_TEST_CLEANUP_LOSS)(
  "cleanup committed response loss",
  async () => {
    const admin = new Pool({ connectionString: url });
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_CLEANUP_LOSS });
    try {
      await seed(admin, "cleanup-loss-ts", "lost", 0);
      await expect(
        new RecordMaintenance(pool, "cleanup-loss-ts", "test", () => 1000).cleanupExpired(1),
      ).rejects.toThrow("outcome_unknown");
      expect(
        (
          await admin.query(
            "SELECT count(*)::int AS n FROM heimdall.records WHERE product_id='cleanup-loss-ts'",
          )
        ).rows[0].n,
      ).toBe(0);
      expect(
        await new RecordMaintenance(admin, "cleanup-loss-ts", "test", () => 1000).cleanupExpired(1),
      ).toBe(0);
    } finally {
      await pool.end();
      await admin.end();
    }
  },
);
