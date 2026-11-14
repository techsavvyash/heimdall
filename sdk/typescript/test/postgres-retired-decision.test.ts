import { recoveryEventId } from "../src/internal/audit-recovery.js";
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
        await expect(
          a.inspectRetentionEligibility(f.tenantId, "z-request", 4001, 1000),
        ).rejects.toThrow("not_found");
        if (scope.resolution === "recovered") {
          const bases = JSON.parse(
            readFileSync(
              new URL("../../../contracts/fixtures/audit-capacity-writes.json", import.meta.url),
              "utf8",
            ),
          );
          for (const event of [bases.terminal, bases.administration])
            Object.assign(event, { productId: scope.productId, environment: scope.environment });
          bases.administration.eventId = recoveryEventId(
            scope.productId,
            scope.environment,
            bases.terminal.eventId,
          );
          await a.recoverRetained(bases.terminal, bases.administration, 1001);
        }
        const payloads = await pool.query(
          "SELECT count(*)::int AS count FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2 AND event_id=ANY($3)",
          [scope.productId, scope.environment, row.ids],
        );
        expect(payloads.rows).toEqual([{ count: 0 }]);
        const admin = await a.inspectRetainedEvent(f.tenantId, f.administrationId);
        expect(admin?.state).toBe("live");
        expect(admin?.event?.eventId).toBe(f.administrationId);
      }
    } finally {
      await pool.end();
    }
  },
);
