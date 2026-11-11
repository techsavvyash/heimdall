import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { recoverPendingDecisions } from "../src/internal/postgres/audit-maintenance.js";
import { buildRecoveryProposal } from "../src/internal/recovery-proposal.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
test.skipIf(!process.env.HEIMDALL_TEST_RECOVERY_BATCH_URL)(
  "bounded recovery survives commit loss and restarts at the pending head",
  async () => {
    const url = process.env.HEIMDALL_TEST_RECOVERY_BATCH_URL;
    const admin = new Pool({ connectionString: url });
    const pool = new Pool({
      connectionString: url,
      options: "-c role=public_capacity_runtime",
    });
    const fault = process.env.HEIMDALL_TEST_RECOVERY_BATCH_LOSS
      ? new Pool({
          connectionString: process.env.HEIMDALL_TEST_RECOVERY_BATCH_LOSS,
          options: "-c role=public_capacity_runtime",
        })
      : undefined;
    const f = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/audit-capacity-writes.json", import.meta.url),
        "utf8",
      ),
    );
    const a = new PostgresAudit(pool, "support", "staging", ["siem"], (event) => {
      validateAuditShape(event);
      validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
    });
    const writer = fault
      ? new PostgresAudit(fault, "support", "staging", ["siem"], (event) => {
          validateAuditShape(event);
          validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
        })
      : a;
    const port = {
      scanPendingDecisions: a.scanPendingDecisions.bind(a),
      readResolution: a.readResolution.bind(a),
      recoverRuntime: writer.recoverRuntime.bind(writer),
    };
    try {
      await admin.query(
        "UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)",
      );
      let proposals = 0;
      const propose = (request: Record<string, unknown>) => {
        proposals++;
        return buildRecoveryProposal(request, {
          terminalId: f.terminal.eventId,
          requestId: f.administration.requestId,
          actor: f.administration.actor,
          at: f.terminal.recordedAt,
          cutoff: f.cutoff,
          sdkVersion: "0.0.0",
        });
      };
      const result = await recoverPendingDecisions(port, f.cutoff, 1, propose);
      expect(result.observed).toHaveLength(1);
      expect(result.observed[0]?.state).toBe("recovered");
      expect(result.more).toBe(false);
      expect(await recoverPendingDecisions(port, f.cutoff, 1, propose)).toEqual({
        observed: [],
        more: false,
      });
      expect(proposals).toBe(1);
      const original = await a.readResolution(f.request.decisionId);
      expect(original?.administration?.actor).toEqual(f.administration.actor);
    } finally {
      await fault?.end();
      await pool.end();
      await admin.end();
    }
  },
);
