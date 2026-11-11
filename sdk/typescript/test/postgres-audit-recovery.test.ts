import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { AuditedDecision } from "../src/internal/audited-decision.js";
import { recoveryEventId } from "../src/internal/audit-recovery.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-recovery-write.json", import.meta.url),
    "utf8",
  ),
);
const url = process.env.HEIMDALL_TEST_RECOVERY_DATABASE_URL;
const validate = (e: Record<string, unknown>) => {
  validateAuditShape(e);
  validateAuditEvent(e as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
const port = (p: Pool, scope: string) =>
  new PostgresAudit(p, scope, "production", ["siem"], validate);
function events(scope: string, id = "recovery-terminal") {
  const request = { ...f.request, productId: scope },
    terminal = { ...f.terminal, productId: scope, eventId: id },
    administration = {
      ...f.administration,
      productId: scope,
      eventId: recoveryEventId(scope, "production", id),
    };
  return { request, terminal, administration };
}
async function counts(p: Pool, scope: string) {
  return (
    await p.query(
      "SELECT (SELECT count(*)::int FROM heimdall.audit_events WHERE product_id=$1) AS events,(SELECT count(*)::int FROM heimdall.audit_deliveries WHERE product_id=$1) AS deliveries,resolution FROM heimdall.audit_decisions WHERE product_id=$1",
      [scope],
    )
  ).rows[0];
}
test("recovery pair validates before database IO", async () => {
  expect(recoveryEventId("ledger", "production", f.terminal.eventId)).toBe(
    f.administration.eventId,
  );
  const p = new Pool();
  try {
    const a = port(p, "ledger");
    for (const patch of f.badAdministration)
      await expect(
        a.recoverDecision(f.terminal, { ...f.administration, ...patch }, f.cutoff),
      ).rejects.toThrow("invalid_argument");
    for (const cutoff of [-1, 1.5, Number.MAX_SAFE_INTEGER + 1, 3000])
      await expect(a.recoverDecision(f.terminal, f.administration, cutoff)).rejects.toThrow(
        "invalid_argument",
      );
    await expect(
      a.recoverDecision(f.terminal, f.administration, f.cutoff, AbortSignal.abort()),
    ).rejects.toThrow("cancelled");
  } finally {
    await p.end();
  }
});
test.skipIf(!url || !!process.env.HEIMDALL_TEST_RECOVERY_PEER)(
  "recovery atomic pair and normal completion races",
  async () => {
    const p = new Pool({ connectionString: url });
    try {
      const scope = "recover-ts",
        a = port(p, scope),
        e = events(scope);
      await a.appendDecision(e.request);
      const receipt = await a.recoverDecision(e.terminal, e.administration, f.cutoff);
      expect(await a.recoverDecision(e.terminal, e.administration, f.cutoff)).toEqual(receipt);
      expect(await counts(p, scope)).toEqual({ events: 3, deliveries: 3, resolution: "recovered" });
      await expect(
        a.appendDecision({ ...e.terminal, eventId: "normal-late", outcome: "allow" }),
      ).rejects.toThrow("conflict");
      await expect(
        a.recoverDecision(
          e.terminal,
          { ...e.administration, actor: { kind: "system", id: "different-job" } },
          f.cutoff,
        ),
      ).rejects.toThrow("conflict");
      expect(await counts(p, scope)).toEqual({ events: 3, deliveries: 3, resolution: "recovered" });
      const n = events("normal-first-ts"),
        normal = port(p, "normal-first-ts");
      await normal.appendDecision(n.request);
      await normal.appendDecision({ ...n.terminal, eventId: "normal", outcome: "allow" });
      await expect(normal.recoverDecision(n.terminal, n.administration, f.cutoff)).rejects.toThrow(
        "conflict",
      );
      expect(await counts(p, "normal-first-ts")).toEqual({
        events: 2,
        deliveries: 2,
        resolution: "completed",
      });
      const recent = events("recent-ts"),
        recentPort = port(p, "recent-ts");
      await recentPort.appendDecision(recent.request);
      await expect(
        recentPort.recoverDecision(recent.terminal, recent.administration, 1000),
      ).rejects.toThrow("conflict");
      expect(await counts(p, "recent-ts")).toEqual({
        events: 1,
        deliveries: 1,
        resolution: "pending",
      });
      const race = events("race-ts"),
        r = port(p, "race-ts");
      await r.appendDecision(race.request);
      const outcomes = await Promise.allSettled([
        r.recoverDecision(race.terminal, race.administration, f.cutoff),
        r.appendDecision({ ...race.terminal, eventId: "normal", outcome: "allow" }),
      ]);
      expect(outcomes.filter((x) => x.status === "fulfilled")).toHaveLength(1);
      expect(outcomes.filter((x) => x.status === "rejected").map((x) => x.reason.message)).toEqual([
        "conflict",
      ]);
      const raceState = await counts(p, "race-ts");
      expect(raceState).toEqual(
        raceState.resolution === "recovered"
          ? { events: 3, deliveries: 3, resolution: "recovered" }
          : { events: 2, deliveries: 2, resolution: "completed" },
      );
      const many = port(p, "many-ts"),
        m = events("many-ts");
      await many.appendDecision(m.request);
      const repeated = await Promise.all(
        Array.from({ length: 8 }, () =>
          many.recoverDecision(m.terminal, m.administration, f.cutoff),
        ),
      );
      expect(repeated.every((x) => JSON.stringify(x) === JSON.stringify(repeated[0]))).toBe(true);
      expect(await counts(p, "many-ts")).toEqual({
        events: 3,
        deliveries: 3,
        resolution: "recovered",
      });
      const competing = port(p, "competing-ts");
      await competing.appendDecision(events("competing-ts").request);
      const attempts = await Promise.allSettled(
        Array.from({ length: 8 }, (_, i) => {
          const pair = events("competing-ts", `attempt-${i}`);
          return competing.recoverDecision(pair.terminal, pair.administration, f.cutoff);
        }),
      );
      expect(attempts.filter((x) => x.status === "fulfilled")).toHaveLength(1);
      expect(attempts.filter((x) => x.status === "rejected").map((x) => x.reason.message)).toEqual(
        Array(7).fill("conflict"),
      );
      expect(await counts(p, "competing-ts")).toEqual({
        events: 3,
        deliveries: 3,
        resolution: "recovered",
      });
      await p.query(
        "CREATE FUNCTION heimdall.reject_recovery_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.product_id LIKE 'recover-fault-%' AND NEW.event_id LIKE 'recovery-%' AND NEW.event_id <> 'recovery-terminal' THEN RAISE EXCEPTION 'test recovery delivery failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_recovery_delivery BEFORE INSERT ON heimdall.audit_deliveries FOR EACH ROW EXECUTE FUNCTION heimdall.reject_recovery_delivery()",
      );
      const bad = events("recover-fault-ts"),
        fault = port(p, "recover-fault-ts");
      await fault.appendDecision(bad.request);
      await expect(
        fault.recoverDecision(bad.terminal, bad.administration, f.cutoff),
      ).rejects.toThrow("unavailable");
      expect(await counts(p, "recover-fault-ts")).toEqual({
        events: 1,
        deliveries: 1,
        resolution: "pending",
      });
      // Simulate imported history with a terminal but no administration record; retry must not fabricate one.
      const missing = events("missing-ts"),
        missingPort = port(p, "missing-ts");
      await missingPort.appendDecision(missing.request);
      await missingPort.append(missing.terminal);
      await p.query(
        "UPDATE heimdall.audit_decisions SET resolution='recovered',terminal_event_id=$2,resolved_at=2001 WHERE product_id=$1",
        ["missing-ts", missing.terminal.eventId],
      );
      await expect(
        missingPort.recoverDecision(missing.terminal, missing.administration, f.cutoff),
      ).rejects.toThrow("audit_unavailable");
      expect(await counts(p, "missing-ts")).toEqual({
        events: 2,
        deliveries: 2,
        resolution: "recovered",
      });
      const lateScope = "recovery-late-ts",
        late = port(p, lateScope);
      let captured: Record<string, unknown> = {};
      const runner = new AuditedDecision(
        lateScope,
        "production",
        async (event, signal) => {
          const receipt = await late.appendDecision(event, signal);
          if (event.eventType === "heimdall.authorization.requested")
            captured = structuredClone(event);
          return receipt;
        },
        validate,
        () => 2001,
      );
      await expect(
        runner.run(events(lateScope).request, async () => {
          const terminal = {
            ...captured,
            eventId: "recovery-terminal",
            eventType: "heimdall.authorization.completed",
            outcome: "outcome_unknown",
            occurredAt: 2001,
            recordedAt: 2001,
          };
          const admin = {
            ...events(lateScope).administration,
            decisionId: captured.decisionId,
            target: { type: "authorization_decision", id: captured.decisionId },
          };
          await late.recoverDecision(terminal, admin, f.cutoff);
          return true;
        }),
      ).rejects.toThrow("audit_unavailable");
      expect(await counts(p, lateScope)).toEqual({
        events: 3,
        deliveries: 3,
        resolution: "recovered",
      });
      await port(p, "recovery-peer-ts").appendDecision(events("recovery-peer-ts").request);
    } finally {
      await p.end();
    }
  },
  20000,
);
test.skipIf(!url || !process.env.HEIMDALL_TEST_RECOVERY_PEER)(
  "recovery TS finishes Go request",
  async () => {
    const p = new Pool({ connectionString: url });
    try {
      const a = port(p, "recovery-peer-go"),
        e = events("recovery-peer-go");
      const receipt = await a.recoverDecision(e.terminal, e.administration, f.cutoff);
      expect(await a.recoverDecision(e.terminal, e.administration, f.cutoff)).toEqual(receipt);
      expect(await counts(p, "recovery-peer-go")).toEqual({
        events: 3,
        deliveries: 3,
        resolution: "recovered",
      });
    } finally {
      await p.end();
    }
  },
);
test.skipIf(!url || !process.env.HEIMDALL_TEST_RECOVERY_LOSS)(
  "recovery committed response loss",
  async () => {
    const p = new Pool({ connectionString: url }),
      fault = new Pool({ connectionString: process.env.HEIMDALL_TEST_RECOVERY_LOSS });
    try {
      const scope = "recovery-loss-ts",
        e = events(scope);
      await port(p, scope).appendDecision(e.request);
      await expect(
        port(fault, scope).recoverDecision(e.terminal, e.administration, f.cutoff),
      ).rejects.toThrow("outcome_unknown");
      expect(await counts(p, scope)).toEqual({ events: 3, deliveries: 3, resolution: "recovered" });
      const receipt = await port(p, scope).recoverDecision(e.terminal, e.administration, f.cutoff);
      expect(receipt.administration.eventId).toBe(e.administration.eventId);
    } finally {
      await fault.end();
      await p.end();
    }
  },
);
