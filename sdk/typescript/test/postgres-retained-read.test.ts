import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
test.skipIf(!process.env.HEIMDALL_TEST_RETAINED_READ_URL)(
  "retained inspection distinguishes retirement from corrupt missing history",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETAINED_READ_URL });
    const port = (environment: string) =>
      new PostgresAudit(pool, "ledger", environment, [], (event) => {
        validateAuditShape(event);
        validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
      });
    const fixture = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/retained-inspection.json", import.meta.url),
        "utf8",
      ),
    );
    const a = port(fixture.environment);
    try {
      for (const c of fixture.cases) {
        const result = await a.inspectRetainedEvent(c.tenantId, c.eventId);
        expect(result?.state ?? "absent").toBe(c.state);
        expect((result?.event ?? null) !== null).toBe(c.state === "live");
        expect(result?.identity.eventId ?? null).toBe(c.state === "absent" ? null : c.eventId);
      }
      await expect(
        a.inspectRetainedEvent("tenant-1", "ordinary", AbortSignal.abort()),
      ).rejects.toThrow("cancelled");
      await pool.query(
        "DELETE FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='production' AND event_id='ordinary'; DELETE FROM heimdall_capacity.event_charges WHERE product_id='ledger' AND environment='production' AND event_id='ordinary'; DELETE FROM heimdall.audit_events WHERE product_id='ledger' AND environment='production' AND event_id='ordinary'",
      );
      await expect(port("production").inspectRetainedEvent("tenant-1", "ordinary")).rejects.toThrow(
        "audit_unavailable",
      );
      await pool.query(
        "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE product_id='ledger' AND environment='staging' AND event_id='normal-terminal'",
      );
      await expect(a.inspectRetainedEvent("tenant-1", "normal-terminal")).rejects.toThrow(
        "audit_unavailable",
      );
      await pool.query("UPDATE heimdall.schema_metadata SET state='migrating'");
      await expect(a.inspectRetainedEvent("tenant-1", "ordinary")).rejects.toThrow("unavailable");
    } finally {
      await pool.end();
    }
  },
);
