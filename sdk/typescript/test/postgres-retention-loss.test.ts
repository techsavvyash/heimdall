import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
test.skipIf(!process.env.HEIMDALL_TEST_RETENTION_LOSS_URL)(
  "retained identities survive a lost COMMIT and cross-language retry",
  async () => {
    const f = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/retention-commit-loss.json", import.meta.url),
        "utf8",
      ),
    );
    const primary = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETENTION_LOSS_URL });
    const writer = new Pool({
      connectionString:
        process.env.HEIMDALL_TEST_RETENTION_FAULT_URL ??
        process.env.HEIMDALL_TEST_RETENTION_LOSS_URL,
    });
    const a = new PostgresAudit(writer, f.productId, f.environment, [], (event) => {
      validateAuditShape(event);
      validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
    });
    try {
      const rows = await primary.query(
        "SELECT request_event_id,terminal_event_id FROM heimdall.audit_decisions WHERE product_id=$1 AND environment=$2",
        [f.productId, f.environment],
      );
      expect(rows.rows).toHaveLength(1);
      const ids = [rows.rows[0].request_event_id, rows.rows[0].terminal_event_id];
      const outcome = await a.retainIdentityGroup(f.tenantId, f.resolution, ids).then(
        () => "success",
        (e: Error) => e.message,
      );
      expect(outcome).toBe(
        process.env.HEIMDALL_TEST_RETENTION_FAULT_URL ? "outcome_unknown" : "success",
      );
      const counts = await primary.query(
        "SELECT (SELECT count(*)::int FROM heimdall_retention.event_identities) AS events,(SELECT count(*)::int FROM heimdall_retention.decision_identities) AS decisions",
      );
      expect(counts.rows[0]).toEqual({ events: f.eventCount, decisions: f.decisionCount });
    } finally {
      await writer.end();
      await primary.end();
    }
  },
);
