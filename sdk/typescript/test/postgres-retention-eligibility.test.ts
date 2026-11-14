import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
test.skipIf(!process.env.HEIMDALL_TEST_RETENTION_ELIGIBILITY_URL)(
  "retention eligibility uses complete primary history and durable protections",
  async () => {
    const f = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/retention-eligibility-read.json", import.meta.url),
        "utf8",
      ),
    );
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_RETENTION_ELIGIBILITY_URL,
    });
    const port = (product: string, environment: string) =>
      new PostgresAudit(pool, product, environment, [], (event) => {
        validateAuditShape(event);
        validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
      });
    try {
      for (const c of f.cases) {
        if (c.before) await pool.query(c.before);
        const before = (await pool.query(f.snapshot)).rows;
        const result = port(c.productId, c.environment).inspectRetentionEligibility(
          c.tenantId,
          c.eventId,
          c.now,
          c.retainForMs,
        );
        const settled = await result.then(
          (value) => ({ value: value ?? "absent" }),
          (error) => ({ error: error.code }),
        );
        expect({ name: c.name, result: settled }).toEqual({
          name: c.name,
          result: c.error ? { error: c.error } : { value: c.expected },
        });
        expect((await pool.query(f.snapshot)).rows).toEqual(before);
        if (c.after) await pool.query(c.after);
      }
      await expect(
        port("ledger", "staging").inspectRetentionEligibility(
          null,
          "ordinary",
          3001,
          1000,
          AbortSignal.abort(),
        ),
      ).rejects.toThrow("cancelled");
    } finally {
      await pool.end();
    }
  },
);
