import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { AuditedDecision } from "../src/internal/audited-decision.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
const base = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/audit-backfill.json", import.meta.url), "utf8"),
);
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-decision-writes.json", import.meta.url),
    "utf8",
  ),
);
const url = process.env.HEIMDALL_TEST_DECISION_DATABASE_URL;
const validate = (e: Record<string, unknown>) => {
  validateAuditShape(e);
  validateAuditEvent(e as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
const port = (p: Pool, scope: string) =>
  new PostgresAudit(p, scope, "production", ["siem"], validate);
const events = (scope: string) =>
  base.events.slice(0, 2).map((e: Record<string, unknown>) => ({ ...e, productId: scope }));
async function counts(p: Pool, scope: string) {
  return (
    await p.query(
      "SELECT (SELECT count(*)::int FROM heimdall.audit_events WHERE product_id=$1) AS events,(SELECT count(*)::int FROM heimdall.audit_deliveries WHERE product_id=$1) AS deliveries,(SELECT count(*)::int FROM heimdall.audit_decisions WHERE product_id=$1) AS decisions",
      [scope],
    )
  ).rows[0];
}
test("decision append rejects recovery through normal path", async () => {
  const p = new Pool();
  try {
    await expect(
      port(p, "input").appendDecision({ ...events("input")[1], outcome: "outcome_unknown" }),
    ).rejects.toThrow("invalid_argument");
    await expect(port(p, "input").appendDecision(base.events[3])).rejects.toThrow(
      "invalid_argument",
    );
    await expect(
      port(p, "input").appendDecision(events("input")[0], AbortSignal.abort()),
    ).rejects.toThrow("cancelled");
  } finally {
    await p.end();
  }
});
test.skipIf(!url || !!process.env.HEIMDALL_TEST_DECISION_PEER)(
  "decision append atomicity and resolution races",
  async () => {
    const p = new Pool({ connectionString: url });
    try {
      for (const outcome of f.normalOutcomes) {
        const scope = `decision-ts-${outcome}`,
          a = port(p, scope),
          [request, terminal] = events(scope);
        terminal.outcome = outcome;
        const first = await a.appendDecision(request);
        expect(await a.appendDecision(request)).toEqual(first);
        const end = await a.appendDecision(terminal);
        expect(await a.appendDecision(terminal)).toEqual(end);
        expect(await a.appendDecision(request)).toEqual(first);
        expect(await counts(p, scope)).toEqual({ events: 2, deliveries: 2, decisions: 1 });
      }
      for (const [i, patch] of f.mismatches.entries()) {
        const scope = `decision-ts-bad-${i}`,
          a = port(p, scope),
          [r, c] = events(scope);
        await a.appendDecision(r);
        await expect(a.appendDecision({ ...c, ...patch })).rejects.toThrow("audit_unavailable");
        expect(await counts(p, scope)).toEqual({ events: 1, deliveries: 1, decisions: 1 });
      }
      const scope = "decision-ts-race",
        a = port(p, scope),
        [r, c] = events(scope);
      const requests = await Promise.all(
        Array.from({ length: f.concurrentWriters }, () => a.appendDecision(r)),
      );
      expect(requests.every((x) => JSON.stringify(x) === JSON.stringify(requests[0]))).toBe(true);
      const results = await Promise.allSettled(
        Array.from({ length: f.concurrentWriters }, (_, i) =>
          a.appendDecision({ ...c, eventId: `terminal-${i}` }),
        ),
      );
      expect(results.filter((x) => x.status === "fulfilled")).toHaveLength(1);
      expect(
        results
          .filter((result) => result.status === "rejected")
          .map((result) => result.reason.message),
      ).toEqual(Array(f.concurrentWriters - 1).fill("conflict"));
      expect(await counts(p, scope)).toEqual({ events: 2, deliveries: 2, decisions: 1 });
      await expect(a.appendDecision({ ...r, eventId: "other-request" })).rejects.toThrow(
        "conflict",
      );
      expect(await counts(p, scope)).toEqual({ events: 2, deliveries: 2, decisions: 1 });
      await expect(
        port(p, "decision-ts-orphan").appendDecision(events("decision-ts-orphan")[1]),
      ).rejects.toThrow("audit_unavailable");
      expect(await counts(p, "decision-ts-orphan")).toEqual({
        events: 0,
        deliveries: 0,
        decisions: 0,
      });
      const corrupt = port(p, "decision-ts-corrupt"),
        [cr, cc] = events("decision-ts-corrupt");
      await corrupt.appendDecision(cr);
      await p.query(
        "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('b',64),'hex') WHERE product_id='decision-ts-corrupt'",
      );
      await expect(corrupt.appendDecision(cc)).rejects.toThrow("audit_unavailable");
      expect(await counts(p, "decision-ts-corrupt")).toEqual({
        events: 1,
        deliveries: 1,
        decisions: 1,
      });
      await p.query(
        "CREATE FUNCTION heimdall.reject_decision_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.product_id LIKE 'decision-%-fault' THEN RAISE EXCEPTION 'test delivery failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_decision_delivery BEFORE INSERT ON heimdall.audit_deliveries FOR EACH ROW EXECUTE FUNCTION heimdall.reject_decision_delivery()",
      );
      await expect(
        port(p, "decision-ts-fault").appendDecision(events("decision-ts-fault")[0]),
      ).rejects.toThrow("unavailable");
      expect(await counts(p, "decision-ts-fault")).toEqual({
        events: 0,
        deliveries: 0,
        decisions: 0,
      });
      const failedScope = "decision-ts-terminal-fault",
        [fr, fc] = events(failedScope);
      await new PostgresAudit(p, failedScope, "production", [], validate).appendDecision(fr);
      await expect(port(p, failedScope).appendDecision(fc)).rejects.toThrow("unavailable");
      expect(await counts(p, failedScope)).toEqual({ events: 1, deliveries: 0, decisions: 1 });
      expect(
        (
          await p.query("SELECT resolution FROM heimdall.audit_decisions WHERE product_id=$1", [
            failedScope,
          ])
        ).rows[0].resolution,
      ).toBe("pending");
      const ro = new Pool({
        connectionString: url,
        options: "-c default_transaction_read_only=on",
      });
      try {
        await expect(
          port(ro, "decision-ts-readonly").appendDecision(events("decision-ts-readonly")[0]),
        ).rejects.toThrow("unavailable");
      } finally {
        await ro.end();
      }
      const lateScope = "decision-ts-late",
        late = port(p, lateScope);
      let captured: Record<string, unknown> = {};
      const runner = new AuditedDecision(
        lateScope,
        "production",
        async (e, s) => {
          const receipt = await late.appendDecision(e, s);
          if (e.eventType === "heimdall.authorization.requested") captured = structuredClone(e);
          return receipt;
        },
        validate,
        () => 2001,
      );
      await expect(
        runner.run(events(lateScope)[0], async () => {
          await late.appendDecision({
            ...captured,
            eventId: "rival-terminal",
            eventType: "heimdall.authorization.completed",
            outcome: "deny",
            recordedAt: 2001,
            occurredAt: 2001,
          });
          return true;
        }),
      ).rejects.toThrow("audit_unavailable");
      expect(await counts(p, lateScope)).toEqual({ events: 2, deliveries: 2, decisions: 1 });
      await port(p, "decision-peer-ts").appendDecision(events("decision-peer-ts")[0]);
    } finally {
      await p.end();
    }
  },
  20000,
);
test.skipIf(!url || !process.env.HEIMDALL_TEST_DECISION_PEER)(
  "decision append TS completes Go request",
  async () => {
    const p = new Pool({ connectionString: url });
    try {
      const a = port(p, "decision-peer-go"),
        [, c] = events("decision-peer-go");
      const receipt = await a.appendDecision(c);
      expect(await a.appendDecision(c)).toEqual(receipt);
      expect(await counts(p, "decision-peer-go")).toEqual({
        events: 2,
        deliveries: 2,
        decisions: 1,
      });
    } finally {
      await p.end();
    }
  },
);
test.skipIf(!url || !process.env.HEIMDALL_TEST_DECISION_LOSS)(
  "decision append committed response loss",
  async () => {
    const direct = new Pool({ connectionString: url }),
      fault = new Pool({ connectionString: process.env.HEIMDALL_TEST_DECISION_LOSS });
    try {
      const stage = process.env.HEIMDALL_TEST_DECISION_STAGE!,
        scope = `decision-loss-ts-${stage}`,
        [r, c] = events(scope);
      if (stage === "terminal") await port(direct, scope).appendDecision(r);
      const event = stage === "terminal" ? c : r;
      await expect(port(fault, scope).appendDecision(event)).rejects.toThrow("outcome_unknown");
      const receipt = await port(direct, scope).appendDecision(event);
      expect(receipt.eventId).toBe(event.eventId);
      expect(await counts(direct, scope)).toEqual({
        events: stage === "terminal" ? 2 : 1,
        deliveries: stage === "terminal" ? 2 : 1,
        decisions: 1,
      });
    } finally {
      await fault.end();
      await direct.end();
    }
  },
);
