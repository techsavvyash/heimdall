import {
  buildRecoveryProposal,
  decodeRecoveryProposal,
} from "../src/internal/recovery-proposal.js";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import { recoveryEventId } from "../src/internal/audit-recovery.js";
test.skipIf(!process.env.HEIMDALL_TEST_RUNTIME_RECOVERY_URL)(
  "v3 maintenance recovers Go history at full capacity",
  async () => {
    const url = process.env.HEIMDALL_TEST_RUNTIME_RECOVERY_URL;
    const admin = new Pool({ connectionString: url });
    const pool = new Pool({ connectionString: url, options: "-c role=public_capacity_runtime" });
    const loss = process.env.HEIMDALL_TEST_RUNTIME_RECOVERY_LOSS;
    const fault = loss ? new Pool({ connectionString: loss }) : undefined;
    const f = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/audit-capacity-writes.json", import.meta.url),
        "utf8",
      ),
    );
    for (const event of [f.terminal, f.administration, f.normal]) {
      event.productId = "support";
      event.environment = "staging";
    }
    f.administration.eventId = recoveryEventId("support", "staging", f.terminal.eventId);
    const make = (p: Pool, product = "support", environment = "staging") =>
      new PostgresAudit(p, product, environment, ["siem"], (event) => {
        validateAuditShape(event);
        validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
      });
    const a = make(pool);
    try {
      const before = (
        await admin.query(
          "SELECT used_events::int AS used,reserved_events::int AS reserved FROM heimdall_capacity.policy",
        )
      ).rows[0];
      await admin.query(
        "UPDATE heimdall_capacity.policy SET event_limit=used_events+reserved_events; UPDATE heimdall_capacity.upgrade SET event_limit=(SELECT event_limit FROM heimdall_capacity.policy)",
      );
      const page = await a.scanPendingDecisions(f.cutoff, null, 1);
      expect(page.items).toHaveLength(1);
      const decisionId = page.items[0]!.request.decisionId as string;
      const history = JSON.parse(
        readFileSync(
          new URL("../../../contracts/fixtures/audit-capacity-backfill.json", import.meta.url),
          "utf8",
        ),
      );
      for (const scope of history.scopes) {
        const record = await make(pool, scope.productId, scope.environment).readResolution(
          decisionId,
        );
        expect(record?.state).toBe(scope.resolution);
        expect(record?.request.productId).toBe(scope.productId);
        expect(record?.request.environment).toBe(scope.environment);
      }
      expect(await make(pool, "unrelated", "production").readResolution(decisionId)).toBeNull();

      expect(await a.readResolution("absent")).toBeNull();
      expect(await a.readResolution(decisionId)).toEqual({
        decisionId,
        state: "pending",
        request: page.items[0]!.request,
        terminal: null,
        administration: null,
      });
      await expect(a.readResolution(decisionId, AbortSignal.abort())).rejects.toThrow("cancelled");
      const proposal = decodeRecoveryProposal(
        page.items[0]!.request,
        JSON.stringify(
          buildRecoveryProposal(page.items[0]!.request, {
            terminalId: f.terminal.eventId,
            requestId: f.administration.requestId,
            actor: f.administration.actor,
            at: f.terminal.recordedAt,
            cutoff: f.cutoff,
            sdkVersion: "0.0.0",
          }),
        ),
      );
      f.terminal = proposal.terminal;
      f.administration = proposal.administration;
      for (const state of ["migrating", "ready"]) {
        await admin.query("UPDATE heimdall.schema_metadata SET database_version=2,state=$1", [
          state,
        ]);
        await expect(a.recoverRuntime(f.terminal, f.administration, f.cutoff)).rejects.toThrow(
          "unavailable",
        );
      }
      await admin.query("UPDATE heimdall.schema_metadata SET database_version=3,state='ready'");
      const lossResult = fault
        ? await make(fault)
            .recoverRuntime(f.terminal, f.administration, f.cutoff)
            .then(
              () => "success",
              (error: Error) => error.message,
            )
        : "not_injected";
      expect(lossResult).toBe(fault ? "outcome_unknown" : "not_injected");
      // A new adapter reads history without receiving the proposal.
      const observed = await make(pool).readResolution(decisionId);
      expect(observed?.state).toBe(fault ? "recovered" : "pending");
      expect(observed?.terminal).toEqual(fault ? f.terminal : null);
      expect(observed?.administration).toEqual(fault ? f.administration : null);
      const receipt = await a.recoverRuntime(f.terminal, f.administration, f.cutoff);
      expect(await a.recoverRuntime(f.terminal, f.administration, f.cutoff)).toEqual(receipt);
      expect((await make(pool).readResolution(decisionId))?.administration).toEqual(
        f.administration,
      );
      await expect(a.appendRuntime(f.normal)).rejects.toThrow("conflict");
      expect(
        (
          await admin.query(
            "SELECT used_events::int AS used,reserved_events::int AS reserved FROM heimdall_capacity.policy",
          )
        ).rows[0],
      ).toEqual({ used: before.used + 2, reserved: before.reserved - 2 });
      expect((await a.scanPendingDecisions(f.cutoff, null, 1)).items).toHaveLength(0);
      const originalDigest = (
        await admin.query(
          "SELECT payload_digest FROM heimdall.audit_events WHERE product_id='support' AND environment='staging' AND event_id=$1",
          [f.administration.eventId],
        )
      ).rows[0].payload_digest;
      await admin.query(
        "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('b',64),'hex') WHERE product_id='support' AND environment='staging' AND event_id=$1",
        [f.administration.eventId],
      );
      await expect(a.readResolution(decisionId)).rejects.toThrow("audit_unavailable");
      await admin.query(
        "UPDATE heimdall.audit_events SET payload_digest=$2 WHERE product_id='support' AND environment='staging' AND event_id=$1",
        [f.administration.eventId, originalDigest],
      );
      await admin.query("UPDATE heimdall.schema_metadata SET state='migrating'");
      await expect(a.readResolution(decisionId)).rejects.toThrow("unavailable");
      await expect(a.recoverRuntime(f.terminal, f.administration, f.cutoff)).rejects.toThrow(
        "unavailable",
      );
    } finally {
      await fault?.end();
      await pool.end();
      await admin.end();
    }
  },
);
