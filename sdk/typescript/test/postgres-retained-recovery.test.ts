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
const port = (pool: Pool) =>
  new PostgresAudit(pool, "ledger", "production", ["siem"], (event) => {
    validateAuditShape(event);
    validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
  });
test.skipIf(!process.env.HEIMDALL_TEST_RETAINED_RECOVERY_URL)(
  "retained recovery is atomic at full quotas",
  async () => {
    const f = read("retained-recovery-writes"),
      pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_RETAINED_RECOVERY_URL }),
      a = port(pool);
    try {
      for (const c of f.cases) {
        if (c.before) await pool.query(c.before);
        const before = (await pool.query(f.snapshot)).rows,
          bases = read("audit-capacity-writes");
        Object.assign(bases.administration, c.adminPatch);
        const input = structuredClone(bases);
        const invoke = c.normal
          ? a.appendRetained(bases.normal)
          : a.recoverRetained(bases.terminal, bases.administration, c.cutoff ?? f.cutoff);
        const result = await invoke.then(
          (receipt) => ({ status: "receipt", receipt }),
          (error) => ({ status: error.code, receipt: null }),
        );
        expect({ name: c.name, status: result.status }).toEqual({
          name: c.name,
          status: c.expected,
        });
        const receipt = (event: Record<string, unknown>) => ({
          productId: "ledger",
          environment: "production",
          eventId: event.eventId,
          recordedAt: event.recordedAt,
          payloadDigest: auditDigest(event),
        });
        expect(result.receipt).toEqual(
          c.expected === "receipt"
            ? { terminal: receipt(bases.terminal), administration: receipt(bases.administration) }
            : null,
        );
        expect(bases).toEqual(input);
        const after = (await pool.query(f.snapshot)).rows;
        expect(c.stable ? after : before).toEqual(before);
        for (const q of c.checks ?? [])
          expect(Object.values((await pool.query(q)).rows[0])).toEqual([true]);
      }
      const bases = read("audit-capacity-writes");
      await expect(
        a.recoverRetained(bases.terminal, bases.administration, f.cutoff, AbortSignal.abort()),
      ).rejects.toThrow("cancelled");
    } finally {
      await pool.end();
    }
  },
);
test.skipIf(!process.env.HEIMDALL_TEST_RETAINED_RECOVERY_LOSS_URL)(
  "retained recovery survives COMMIT response loss and peer retry",
  async () => {
    const url = process.env.HEIMDALL_TEST_RETAINED_RECOVERY_LOSS_URL,
      fault = process.env.HEIMDALL_TEST_RETAINED_RECOVERY_FAULT_URL;
    const primary = new Pool({ connectionString: url }),
      writer = new Pool({ connectionString: fault ?? url });
    try {
      const bases = read("audit-capacity-writes");
      const status = await port(writer)
        .recoverRetained(bases.terminal, bases.administration, 1001)
        .then(
          () => "receipt",
          (e) => e.code,
        );
      expect(status).toBe(fault ? "outcome_unknown" : "receipt");
      const result = await primary.query(
        "SELECT d.resolution='recovered' AND (SELECT count(*) FROM heimdall_retention.event_identities i WHERE i.product_id=d.product_id AND i.environment=d.environment AND i.decision_id=d.decision_id)=3 AS valid FROM heimdall_retention.decision_identities d WHERE d.product_id='ledger' AND d.environment='production' AND d.decision_id='decision-1'",
      );
      expect(result.rows).toEqual([{ valid: true }]);
    } finally {
      await writer.end();
      await primary.end();
    }
  },
);
