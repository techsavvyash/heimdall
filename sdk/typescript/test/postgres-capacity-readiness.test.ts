import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import {
  capacityChecksum,
  capacitySQL,
  checkCapacityReadiness,
  checkManagedCapacityReadiness,
  checkAuditReadiness,
} from "../src/internal/postgres/readiness.js";
const root = new URL("../../../contracts/", import.meta.url);
const fixture = JSON.parse(
  readFileSync(new URL("fixtures/audit-capacity-readiness.json", root), "utf8"),
) as { cases: { name: string; sql: string[]; ready: boolean; managedReady: boolean }[] };
test("capacity protocol binds catalog, Go query and cancellation", async () => {
  const catalog = JSON.parse(readFileSync(new URL("persistence/catalog.json", root), "utf8"));
  expect(catalog.session[2].checksum).toBe(capacityChecksum);
  const go = readFileSync(
    new URL("../../go/internal/postgres/audit_capacity_activate.go", import.meta.url),
    "utf8",
  );
  expect(go).toContain("const capacityReadinessSQL = `" + capacitySQL + "`");
  const pool = new Pool();
  try {
    for (const check of [checkCapacityReadiness, checkManagedCapacityReadiness])
      await expect(check(pool, AbortSignal.abort())).rejects.toThrow("cancelled");
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_CAPACITY_READY_URL)(
  "capacity readiness matches shared mutations",
  async () => {
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_CAPACITY_READY_URL,
      max: 1,
    });
    try {
      await checkCapacityReadiness(pool);
      await expect(checkAuditReadiness(pool)).rejects.toThrow("unavailable");
      await checkManagedCapacityReadiness(pool);
      for (const c of fixture.cases) {
        await pool.query("BEGIN");
        try {
          for (const sql of c.sql) await pool.query(sql);
          const result = await checkCapacityReadiness(pool).then(
            () => true,
            () => false,
          );
          expect({ name: c.name, result }).toEqual({ name: c.name, result: c.ready });
          const managed = await checkManagedCapacityReadiness(pool).then(
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
