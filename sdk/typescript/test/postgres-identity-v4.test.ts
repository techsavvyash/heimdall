import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { createPostgresStorage } from "../src/postgres.js";
import type { RuntimeStorage } from "../src/runtime.js";
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/public-runtime-v4.json", import.meta.url),
    "utf8",
  ),
) as {
  identity: Parameters<RuntimeStorage["identity"]["check"]>[0];
  negatives: Parameters<RuntimeStorage["identity"]["invalidate"]>[0][];
  legacy: string[];
};
test.skipIf(!process.env.HEIMDALL_TEST_FACTORY_URL)(
  "public v4 identity admission",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_FACTORY_URL });
    const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_FACTORY_ADMIN, max: 1 });
    const storage = (pool: Pool, environment = "identity-ts") =>
      createPostgresStorage(pool, { productId: "ledger", environment, auditSinks: ["siem"] });
    const port = storage(pool),
      signal = new AbortController().signal;
    const refused = async (p: RuntimeStorage) => {
      await expect(p.identity.check(f.identity, signal)).rejects.toMatchObject({
        code: "unavailable",
      });
      for (const n of f.negatives)
        await expect(p.identity.invalidate(n, signal)).rejects.toMatchObject({
          code: "unavailable",
        });
    };
    try {
      for (const key of f.legacy) {
        const other = new Pool({ connectionString: process.env[key] });
        try {
          await refused(storage(other));
        } finally {
          await other.end();
        }
      }
      const readonly = new Pool({
        connectionString:
          process.env.HEIMDALL_TEST_FACTORY_URL + "%20-c%20default_transaction_read_only%3Don",
      });
      try {
        await refused(storage(readonly));
      } finally {
        await readonly.end();
      }
      await admin.query("UPDATE heimdall.schema_metadata SET state='migrating'");
      await refused(port);
      await admin.query("UPDATE heimdall.schema_metadata SET state='ready'");
      await admin.query("BEGIN; SELECT pg_advisory_xact_lock(74162401)");
      for (const n of f.negatives)
        await expect(port.identity.invalidate(n, AbortSignal.timeout(100))).rejects.toMatchObject({
          code: "deadline_exceeded",
        });
      await admin.query("ROLLBACK");
      expect(
        (
          await admin.query(
            "SELECT count(*)::int AS n FROM heimdall.identity_denials WHERE environment='identity-ts'",
          )
        ).rows[0].n,
      ).toBe(0);
      expect(await port.identity.check(f.identity, signal)).toBe(false);
      for (const n of f.negatives) await port.identity.invalidate(n, signal);
      expect(await port.identity.check(f.identity, signal)).toBe(true);
      expect(await storage(pool, "identity-ts-other").identity.check(f.identity, signal)).toBe(
        false,
      );
    } finally {
      await admin.query("ROLLBACK; UPDATE heimdall.schema_metadata SET state='ready'");
      await pool.end();
      await admin.end();
    }
  },
  15000,
);
