import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
const fixture = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/managed-schema.json", import.meta.url), "utf8"),
) as {
  setup: string[];
  cases: { name: string; sql: string; sqlState: string }[];
  payload: string;
  principal: string;
  identityHash: string;
  rollback: string[];
  terminal: string;
  terminalConflict: string;
  successor: string;
};
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("managed SQL scope immutability and transaction boundaries", async () => {
  const pool = new Pool({
    connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
    options: "-c role=heimdall_managed_runtime",
  });
  const sql = (value: string) => value.replaceAll("$scope", "managed-ts");
  try {
    for (const command of fixture.setup) await pool.query(sql(command));
    for (const c of fixture.cases) {
      const code = await pool.query(sql(c.sql)).then(
        () => "accepted",
        (e: { code: string }) => e.code,
      );
      expect({ name: c.name, code }).toEqual({ name: c.name, code: c.sqlState });
    }
    const row = (
      await pool.query(
        "SELECT intent_payload FROM heimdall_managed.operations WHERE product_id='managed-ts' AND operation_id='original'",
      )
    ).rows[0];
    expect(row.intent_payload.toString("utf8")).toBe(fixture.payload);
    const before = (
      await pool.query(
        "SELECT position::text,revision::text FROM heimdall_managed.membership_candidates WHERE product_id='managed-ts'",
      )
    ).rows[0];
    await pool.query(
      "UPDATE heimdall_managed.membership_candidates SET observed_at=101 WHERE product_id='managed-ts'",
    );
    const after = (
      await pool.query(
        "SELECT position::text,revision::text FROM heimdall_managed.membership_candidates WHERE product_id='managed-ts'",
      )
    ).rows[0];
    expect(after.position).toBe(before.position);
    expect(BigInt(after.revision) > BigInt(before.revision)).toBe(true);
    const client = await pool.connect();
    try {
      await client.query("BEGIN");
      for (const command of fixture.rollback) await client.query(sql(command));
      await client.query("ROLLBACK");
    } finally {
      client.release();
    }
    expect(
      (
        await pool.query(
          "SELECT count(*)::int AS count FROM heimdall_managed.workspaces WHERE product_id='managed-ts' AND workspace_id='rollback'",
        )
      ).rows[0].count,
    ).toBe(0);
    expect(
      (
        await pool.query(
          "SELECT count(*)::int AS count FROM heimdall_managed.operations WHERE product_id='managed-ts' AND operation_id='rollback-op'",
        )
      ).rows[0].count,
    ).toBe(0);
    expect(
      (
        await pool.query(
          "SELECT count(*)::int AS count FROM heimdall_managed.membership_candidates WHERE product_id='managed-ts' AND workspace_id='rollback'",
        )
      ).rows[0].count,
    ).toBe(0);
    for (const table of ["audit_events", "audit_deliveries"]) {
      expect(
        (
          await pool.query(
            `SELECT count(*)::int AS count FROM heimdall.${table} WHERE product_id='managed-ts' AND event_id='managed-rollback'`,
          )
        ).rows[0].count,
      ).toBe(0);
    }
    await pool.query(sql(fixture.terminal));
    await expect(pool.query(sql(fixture.terminalConflict))).rejects.toMatchObject({
      code: "23505",
    });
    await pool.query(sql(fixture.successor));
  } finally {
    await pool.end();
  }
});

test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("managed SQL reads Go intent bytes", async () => {
  const pool = new Pool({
    connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
    options: "-c role=heimdall_managed_runtime",
  });
  try {
    const row = (
      await pool.query(
        "SELECT intent_payload FROM heimdall_managed.operations WHERE product_id='managed-go' AND operation_id='original'",
      )
    ).rows[0];
    expect(row.intent_payload.toString("utf8")).toBe(fixture.payload);
  } finally {
    await pool.end();
  }
});
