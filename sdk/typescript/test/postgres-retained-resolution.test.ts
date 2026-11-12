import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/retained-resolution.json", import.meta.url),
    "utf8",
  ),
) as Record<
  "live" | "retired",
  {
    name: string;
    product: string;
    environment: string;
    id: string;
    before?: string;
    expected: string;
  }[]
>;
test.skipIf(!process.env.HEIMDALL_TEST_RETAINED_RESOLUTION_URL)(
  "retained decision resolution",
  async () => {
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_RETAINED_RESOLUTION_URL,
      max: 1,
    });
    const retired = process.env.HEIMDALL_TEST_RETAINED_RESOLUTION_RETIRED === "1";
    const validate = (event: Record<string, unknown>) => {
      validateAuditShape(event);
      validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
    };
    try {
      for (const c of fixture[retired ? "retired" : "live"]) {
        await pool.query("BEGIN");
        try {
          if (c.before) await pool.query(c.before);
          const legacy = new PostgresAudit(pool, c.product, c.environment, ["siem"], validate);
          const read =
            retired && process.env.HEIMDALL_TEST_RETAINED_RESOLUTION_V4 !== "1"
              ? legacy.readRetainedResolution.bind(legacy)
              : PostgresAudit.forRetainedRuntime(pool, c.product, c.environment, ["siem"], validate)
                  .readResolution;
          const result = await read(c.id).then(
            (value) => ({ status: value?.state ?? "absent", value }),
            (error) => ({ status: (error as Error).message, value: null }),
          );
          expect({ name: c.name, status: result.status }).toEqual({
            name: c.name,
            status: c.expected,
          });
          if (result.value) {
            assert.equal(result.value.decisionId, c.id);
            assert.equal(result.value.request.eventId, "z-request");
          }
        } finally {
          await pool.query("ROLLBACK");
        }
      }
      const a = PostgresAudit.forRetainedRuntime(pool, "ledger", "production", ["siem"], validate);
      await expect(a.readResolution("decision-1", AbortSignal.abort())).rejects.toMatchObject({
        code: "cancelled",
      });
    } finally {
      await pool.end();
    }
  },
);
