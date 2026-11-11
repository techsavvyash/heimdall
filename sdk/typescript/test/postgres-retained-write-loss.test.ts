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
test.skipIf(!process.env.HEIMDALL_TEST_RETAINED_WRITE_LOSS_URL)(
  "retained append survives response loss and peer retry without rerouting",
  async () => {
    const c = read("retained-write-loss").find(
      (c: { name: string }) => c.name === process.env.HEIMDALL_TEST_RETAINED_WRITE_CASE,
    );
    if (!c) throw new Error("unknown case");
    const event = { ...read("audit-capacity-writes")[c.base], ...c.patch };
    const expected = {
      productId: "ledger",
      environment: "production",
      eventId: event.eventId,
      recordedAt: event.recordedAt,
      payloadDigest: auditDigest(event),
    };
    const url = process.env.HEIMDALL_TEST_RETAINED_WRITE_LOSS_URL,
      fault = process.env.HEIMDALL_TEST_RETAINED_WRITE_FAULT_URL;
    const primary = new Pool({ connectionString: url }),
      writer = new Pool({ connectionString: fault ?? url });
    const port = (pool: Pool, sinks: string[]) =>
      new PostgresAudit(pool, "ledger", "production", sinks, (event) => {
        validateAuditShape(event);
        validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
      });
    try {
      const result = await port(writer, [fault ? "siem" : "changed-sink"])
        .appendRetained(event)
        .then(
          (receipt) => ({ status: "receipt", receipt }),
          (e) => ({ status: e.code, receipt: null }),
        );
      expect(result).toEqual(
        fault
          ? { status: "outcome_unknown", receipt: null }
          : { status: "receipt", receipt: expected },
      );
      const reader = port(primary, []);
      expect(await reader.resolveRetainedReceipt(event)).toEqual(expected);
      const inspection = await reader.inspectRetainedEvent("tenant-1", event.eventId);
      expect(inspection?.state).toBe("live");
      expect(inspection?.identity.routing).toEqual(["siem"]);
    } finally {
      await writer.end();
      await primary.end();
    }
  },
);
