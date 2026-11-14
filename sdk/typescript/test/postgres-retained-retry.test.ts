import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { auditDigest } from "../src/internal/audit-receipts.js";
import { recoveryEventId } from "../src/internal/audit-recovery.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
const read = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const bases = read("audit-capacity-writes");
for (const c of read("retained-retry"))
  for (const mode of c.expected === "absent" ? ["resolve"] : ["resolve", "append"])
    test.skipIf(!process.env.HEIMDALL_TEST_RETAINED_RETRY_URL)(
      `retained retry ${mode}: ${c.name}`,
      async () => {
        const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETAINED_RETRY_URL });
        try {
          const a = new PostgresAudit(pool, c.productId, c.environment, [], (event) => {
            validateAuditShape(event);
            validateAuditEvent(
              event as Parameters<typeof validateAuditEvent>[0],
              "sdk",
              () => false,
            );
          });
          const event = {
            ...structuredClone(bases[c.base]),
            productId: c.productId,
            environment: c.environment,
          };
          if (c.base === "administration")
            event.eventId = recoveryEventId(c.productId, c.environment, bases.terminal.eventId);
          Object.assign(event, c.patch);
          const before = structuredClone(event);
          const result = await (
            mode === "resolve" ? a.resolveRetainedReceipt(event) : a.appendRetained(event)
          ).then(
            (receipt) => ({ status: receipt ? "receipt" : "absent", receipt }),
            (e: Error) => ({ status: e.message, receipt: null }),
          );
          expect(result.status).toBe(c.expected);
          const expected =
            c.expected === "receipt"
              ? {
                  productId: c.productId,
                  environment: c.environment,
                  eventId: event.eventId,
                  recordedAt: event.recordedAt,
                  payloadDigest: auditDigest(event),
                }
              : null;
          expect(result.receipt).toEqual(expected);
          expect(event).toEqual(before);
        } finally {
          await pool.end();
        }
      },
    );
