import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { checkReadiness } from "../src/internal/postgres/readiness.js";
const root = new URL("../../../contracts/", import.meta.url);
const catalog = JSON.parse(readFileSync(new URL("persistence/catalog.json", root), "utf8"));
const cases = JSON.parse(
  readFileSync(new URL("fixtures/postgres-readiness.json", root), "utf8"),
) as { name: string; sql: string[]; ready: boolean }[];
test("readiness cancellation requires no database connection", async () => {
  const pool = new Pool();
  try {
    await expect(checkReadiness(pool, AbortSignal.abort())).rejects.toThrow("cancelled");
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "live startup compatibility matrix",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    try {
      for (const item of cases) {
        await pool.query("DELETE FROM heimdall.schema_metadata; DELETE FROM heimdall.migrations");
        await pool.query(
          "INSERT INTO heimdall.schema_metadata(database_version,state,reader_min,reader_max,writer_min,writer_max) VALUES(1,'ready',1,1,1,1)",
        );
        await pool.query("INSERT INTO heimdall.migrations(version,checksum) VALUES(1,$1)", [
          catalog.session[0].checksum,
        ]);
        for (const sql of item.sql) await pool.query(sql);
        const result = await checkReadiness(pool).then(
          () => true,
          (error) => error.message,
        );
        expect({ name: item.name, result }).toEqual({
          name: item.name,
          result: item.ready ? true : "unavailable",
        });
      }
      await pool.query("DELETE FROM heimdall.migrations");
      await pool.query("INSERT INTO heimdall.migrations(version,checksum) VALUES(1,$1)", [
        catalog.session[0].checksum,
      ]);
      const migration = await pool.connect();
      try {
        await migration.query("BEGIN");
        await migration.query("UPDATE heimdall.schema_metadata SET state='migrating'");
        await migration.query("UPDATE heimdall.migrations SET checksum=repeat('0',64)");
        await expect(checkReadiness(pool)).resolves.toBeUndefined();
        await migration.query("COMMIT");
        await expect(checkReadiness(pool)).rejects.toThrow("unavailable");
      } finally {
        await migration.query("ROLLBACK");
        migration.release();
      }
      await pool.query("UPDATE heimdall.schema_metadata SET state='ready'");
      await pool.query("UPDATE heimdall.migrations SET checksum=$1", [catalog.session[0].checksum]);
      const readOnly = new Pool({
        connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
        options: "-c default_transaction_read_only=on",
      });
      try {
        expect(
          (await readOnly.query("SHOW transaction_read_only")).rows[0].transaction_read_only,
        ).toBe("on");
        await expect(checkReadiness(readOnly)).rejects.toThrow("unavailable");
      } finally {
        await readOnly.end();
      }
      await pool.query("ALTER TABLE heimdall.schema_metadata RENAME TO hidden_metadata");
      try {
        await expect(checkReadiness(pool)).rejects.toThrow("unavailable");
      } finally {
        await pool.query("ALTER TABLE heimdall.hidden_metadata RENAME TO schema_metadata");
      }
    } finally {
      await pool.end();
    }
  },
  15000,
);
