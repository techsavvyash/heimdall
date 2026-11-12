import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import {
  retentionChecksums,
  retentionSQL,
  checkRetentionReadiness,
  checkManagedRetentionReadiness,
  checkAuditReadiness,
  checkCapacityReadiness,
} from "../src/internal/postgres/readiness.js";
const root = new URL("../../../contracts/", import.meta.url);
const fixture = JSON.parse(
  readFileSync(new URL("fixtures/audit-retention-readiness.json", root), "utf8"),
) as { cases: { name: string; sql: string[]; ready: boolean; managedReady: boolean }[] };
test("retention protocol binds catalog, Go query and cancellation", async () => {
  const catalog = JSON.parse(readFileSync(new URL("persistence/catalog.json", root), "utf8"));
  expect(catalog.session[3].checksum).toBe(retentionChecksums[0]);
  const go = readFileSync(
    new URL("../../go/internal/postgres/audit_retention_activate.go", import.meta.url),
    "utf8",
  );
  expect(go).toContain("const retentionReadinessSQL = `" + retentionSQL + "`");
  const pool = new Pool();
  try {
    for (const check of [checkRetentionReadiness, checkManagedRetentionReadiness])
      await expect(check(pool, AbortSignal.abort())).rejects.toThrow("cancelled");
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_RETENTION_READY_URL)(
  "retention readiness matches shared mutations",
  async () => {
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_RETENTION_READY_URL,
      max: 1,
    });
    try {
      await checkRetentionReadiness(pool);
      await expect(checkAuditReadiness(pool)).rejects.toThrow("unavailable");
      await expect(checkCapacityReadiness(pool)).rejects.toThrow("unavailable");
      await checkManagedRetentionReadiness(pool);
      for (const c of fixture.cases) {
        await pool.query("BEGIN");
        try {
          for (const sql of c.sql) await pool.query(sql);
          const result = await checkRetentionReadiness(pool).then(
            () => true,
            () => false,
          );
          expect({ name: c.name, result }).toEqual({ name: c.name, result: c.ready });
          const managed = await checkManagedRetentionReadiness(pool).then(
            () => true,
            () => false,
          );
          expect({ name: c.name, managed }).toEqual({ name: c.name, managed: c.managedReady });
        } finally {
          await pool.query("ROLLBACK");
        }
      }
    } finally {
      await pool.end();
    }
  },
);
