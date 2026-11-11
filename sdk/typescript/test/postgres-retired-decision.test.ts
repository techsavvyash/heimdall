import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
test.skipIf(!process.env.HEIMDALL_TEST_RETIRED_DECISION_URL)(
  "all completed and recovered decision members remain inspectable after retirement",
  async () => {
    const f = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/retired-decision-inspection.json", import.meta.url),
        "utf8",
      ),
    );
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETIRED_DECISION_URL });
    try {
      for (const scope of f.scopes) {
        const rows = await pool.query(
          "SELECT array_remove(ARRAY[request_event_id,terminal_event_id,recovery_event_id],NULL) AS ids,resolution FROM heimdall_retention.decision_identities WHERE product_id=$1 AND environment=$2",
          [scope.productId, scope.environment],
        );
        expect(rows.rows).toHaveLength(1);
        const row = rows.rows[0];
        expect(row.ids).toHaveLength(scope.members);
        expect(row.resolution).toBe(scope.resolution);
        const a = new PostgresAudit(pool, scope.productId, scope.environment, [], (event) => {
          validateAuditShape(event);
          validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
        });
        for (const id of row.ids) {
          const result = await a.inspectRetainedEvent(f.tenantId, id);
          expect(result?.state).toBe("retired");
          expect(result?.event).toBeNull();
          expect(result?.identity.eventId).toBe(id);
          expect(result?.identity.decisionId).toBe("decision-1");
        }
        const admin = await a.inspectRetainedEvent(f.tenantId, f.administrationId);
        expect(admin?.state).toBe("live");
        expect(admin?.event?.eventId).toBe(f.administrationId);
      }
    } finally {
      await pool.end();
    }
  },
);
