import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import { auditDigest } from "../src/internal/audit-receipts.js";
const read = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
test.skipIf(!process.env.HEIMDALL_TEST_RETAINED_WRITE_URL)(
  "retained append commits payload and metadata atomically",
  async () => {
    const f = read("retained-writes"),
      bases = read("audit-capacity-writes");
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETAINED_WRITE_URL });
    const a = new PostgresAudit(pool, "ledger", "staging", ["siem"], (event) => {
      validateAuditShape(event);
      validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
    });
    try {
      for (const c of f.cases) {
        if (c.before) await pool.query(c.before);
        const before = (await pool.query(f.snapshot)).rows;
        const event = { ...structuredClone(bases[c.base]), ...c.patch },
          input = structuredClone(event);
        const result = await a.appendRetained(event).then(
          (receipt) => ({ status: "receipt", receipt }),
          (error) => ({ status: error.code, receipt: null }),
        );
        expect({ name: c.name, status: result.status }).toEqual({
          name: c.name,
          status: c.expected,
        });
        expect(result.receipt).toEqual(
          c.expected === "receipt"
            ? {
                productId: "ledger",
                environment: "staging",
                eventId: event.eventId,
                recordedAt: event.recordedAt,
                payloadDigest: auditDigest(event),
              }
            : null,
        );
        expect(event).toEqual(input);
        const after = (await pool.query(f.snapshot)).rows;
        expect(c.stable ? after : before).toEqual(before);
        for (const check of c.checks)
          expect(Object.values((await pool.query(check)).rows[0])).toEqual([true]);
      }
      await expect(
        a.appendRetained({ ...bases.ordinary, environment: "staging" }, AbortSignal.abort()),
      ).rejects.toThrow("cancelled");
    } finally {
      await pool.end();
    }
  },
);
