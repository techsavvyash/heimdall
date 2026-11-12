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
  const upgrade = readFileSync(
    new URL("../../go/internal/postgres/audit_hold_release_upgrade.go", import.meta.url),
    "utf8",
  );
  expect(upgrade).toContain(
    `const holdReleaseMigrationChecksum = "${catalog.session[4].checksum}"`,
  );
  const goString = (name: string) => {
    const marker = "const " + name + " = `";
    const start = go.indexOf(marker);
    if (start === -1) throw new Error("missing Go readiness definition");
    return go.slice(start + marker.length).split(/`\n(?=\n|const )/)[0]!;
  };
  const v5 = goString("retentionV5ProtocolSQL").replace(
    "` + holdReleaseMigrationChecksum + `",
    catalog.session[4].checksum,
  );
  const composed = goString("retentionV4ReadinessSQL").replace(
    "` + retentionV4ProtocolSQL + `",
    `(${goString("retentionV4ProtocolSQL")} OR ${v5})`,
  );
  const normalize = (value: string) => value.replace(/\s+/g, " ").trim();
  expect(normalize(composed)).toBe(normalize(retentionSQL));
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
