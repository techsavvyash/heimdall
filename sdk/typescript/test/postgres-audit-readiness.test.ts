import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import {
  checkAuditReadiness,
  checkReadiness,
  decisionChecksum,
} from "../src/internal/postgres/readiness.js";
const root = new URL("../../../contracts/", import.meta.url);
const fixture = JSON.parse(
  readFileSync(new URL("fixtures/audit-activation.json", root), "utf8"),
) as { readiness: { name: string; sql: string[]; ready: boolean }[] };
test("v2 readiness binds the catalog and preserves cancellation", async () => {
  const catalog = JSON.parse(readFileSync(new URL("persistence/catalog.json", root), "utf8"));
  expect(catalog.session[1].checksum).toBe(decisionChecksum);
  const pool = new Pool();
  try {
    await expect(checkAuditReadiness(pool, AbortSignal.abort())).rejects.toThrow("cancelled");
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_ACTIVATION_URL)(
  "Go activated storage is accepted only by the matching protocol",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_ACTIVATION_URL, max: 1 });
    try {
      await expect(checkAuditReadiness(pool)).resolves.toBeUndefined();
      await expect(checkReadiness(pool)).rejects.toThrow("unavailable");
      for (const c of fixture.readiness) {
        await pool.query("BEGIN");
        try {
          for (const sql of c.sql) await pool.query(sql);
          const result = await checkAuditReadiness(pool).then(
            () => true,
            (e) => e.message,
          );
          expect({ name: c.name, result }).toEqual({
            name: c.name,
            result: c.ready ? true : "unavailable",
          });
        } finally {
          await pool.query("ROLLBACK");
        }
      }
      const ro = new Pool({
        connectionString: process.env.HEIMDALL_TEST_ACTIVATION_URL,
        options: "-c default_transaction_read_only=on",
      });
      try {
        await expect(checkAuditReadiness(ro)).rejects.toThrow("unavailable");
      } finally {
        await ro.end();
      }
    } finally {
      await pool.end();
    }
  },
  15000,
);
