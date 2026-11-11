import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { checkManagedReadiness, checkReadiness } from "../src/internal/postgres/readiness.js";
const root = new URL("../../../contracts/", import.meta.url);
const read = (path: string) => JSON.parse(readFileSync(new URL(path, root), "utf8"));
const cases = read("fixtures/managed-readiness.json") as {
  name: string;
  sql: string[];
  ready: boolean;
}[];
const catalog = read("persistence/catalog.json");
test("managed readiness cancellation before connection", async () => {
  const pool = new Pool();
  try {
    await expect(checkManagedReadiness(pool, AbortSignal.abort())).rejects.toThrow("cancelled");
  } finally {
    await pool.end();
  }
});
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !!process.env.HEIMDALL_TEST_MANAGED_MISSING,
)("managed readiness checks both families", async () => {
  const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  async function reset() {
    for (const family of ["session", "managed"]) {
      const schema = family === "session" ? "heimdall" : "heimdall_managed";
      await pool.query(
        `DELETE FROM ${schema}.schema_metadata; DELETE FROM ${schema}.migrations; INSERT INTO ${schema}.schema_metadata VALUES(true,1,'ready',1,1,1,1)`,
      );
      await pool.query(`INSERT INTO ${schema}.migrations(version,checksum) VALUES(1,$1)`, [
        catalog[family][0].checksum,
      ]);
    }
  }
  try {
    for (const c of cases) {
      await reset();
      for (const sql of c.sql) await pool.query(sql);
      const result = await checkManagedReadiness(pool).then(
        () => true,
        () => false,
      );
      expect({ name: c.name, result }).toEqual({ name: c.name, result: c.ready });
    }
    await reset();
    const connection = await pool.connect();
    try {
      await connection.query(
        "BEGIN; UPDATE heimdall_managed.schema_metadata SET state='migrating'",
      );
      await expect(checkManagedReadiness(pool)).resolves.toBeUndefined();
      await connection.query("COMMIT");
      await expect(checkManagedReadiness(pool)).rejects.toThrow("unavailable");
      await expect(checkReadiness(pool)).resolves.toBeUndefined();
    } finally {
      await connection.query("ROLLBACK");
      connection.release();
    }
    await reset();
    const restricted = new Pool({
      connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
      options: "-c role=heimdall_managed_status",
    });
    const readonly = new Pool({
      connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
      options: "-c default_transaction_read_only=on",
    });
    try {
      await expect(checkManagedReadiness(restricted)).resolves.toBeUndefined();
      await expect(checkManagedReadiness(readonly)).rejects.toThrow("unavailable");
    } finally {
      await restricted.end();
      await readonly.end();
    }
  } finally {
    await reset();
    await pool.end();
  }
});

test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !process.env.HEIMDALL_TEST_MANAGED_MISSING,
)("missing managed schema fails without changing session readiness", async () => {
  const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  try {
    await expect(checkReadiness(pool)).resolves.toBeUndefined();
    await expect(checkManagedReadiness(pool)).rejects.toThrow("unavailable");
    expect(
      (
        await pool.query(
          "SELECT count(*)::int AS count FROM pg_namespace WHERE nspname='heimdall_managed'",
        )
      ).rows[0].count,
    ).toBe(0);
  } finally {
    await pool.end();
  }
});
