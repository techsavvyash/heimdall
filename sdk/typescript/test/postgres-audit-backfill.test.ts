import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
const f = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/audit-backfill.json", import.meta.url), "utf8"),
);
const url = process.env.HEIMDALL_TEST_BACKFILL_DATABASE_URL;
const validate = (event: Record<string, unknown>) => {
  validateAuditShape(event);
  validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
const port = (p: Pool, scope: string) =>
  new PostgresAudit(p, scope, "production", ["siem"], validate);
async function seed(p: Pool, scope: string, events = f.events) {
  for (const event of events) await port(p, scope).append({ ...event, productId: scope });
  await p.query(
    "INSERT INTO heimdall.audit_decision_backfill(product_id,environment) VALUES($1,'production')",
    [scope],
  );
}
async function state(p: Pool, scope: string) {
  return (
    await p.query(
      "SELECT phase,last_event_id,scanned_count::text FROM heimdall.audit_decision_backfill WHERE product_id=$1",
      [scope],
    )
  ).rows[0];
}
async function finish(p: Pool, scope: string, start = 0) {
  for (const step of f.steps.slice(start))
    expect(await port(p, scope).backfillDecisionPage(1)).toEqual(step);
}
test("audit backfill input and cancellation", async () => {
  const p = new Pool();
  try {
    for (const limit of [0, 101, 1.5])
      await expect(port(p, "backfill").backfillDecisionPage(limit)).rejects.toThrow(
        "invalid_argument",
      );
    await expect(port(p, "backfill").backfillDecisionPage(1, AbortSignal.abort())).rejects.toThrow(
      "cancelled",
    );
  } finally {
    await p.end();
  }
});
test.skipIf(!url || !!process.env.HEIMDALL_TEST_BACKFILL_PEER)(
  "audit backfill transaction checkpoints and failures",
  async () => {
    const p = new Pool({ connectionString: url });
    try {
      await seed(p, "backfill-ts");
      await finish(p, "backfill-ts");
      expect(await port(p, "backfill-ts").backfillDecisionPage(100)).toEqual(f.steps.at(-1));
      expect(
        (
          await p.query(
            "SELECT resolution FROM heimdall.audit_decisions WHERE product_id='backfill-ts' ORDER BY decision_id",
          )
        ).rows.map((r) => r.resolution),
      ).toEqual(["completed", "pending"]);
      expect(
        (
          await p.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_events WHERE product_id='backfill-ts'",
          )
        ).rows[0].n,
      ).toBe(4);
      expect(
        (
          await p.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE product_id='backfill-ts'",
          )
        ).rows[0].n,
      ).toBe(4);
      await seed(p, "backfill-peer-ts");
      for (const step of f.steps.slice(0, 2))
        expect(await port(p, "backfill-peer-ts").backfillDecisionPage(1)).toEqual(step);
      const request = f.events[0],
        terminal = f.events[1];
      await seed(p, "backfill-duplicate-ts", [request, { ...request, eventId: "other-request" }]);
      await expect(port(p, "backfill-duplicate-ts").backfillDecisionPage(100)).rejects.toThrow(
        "audit_unavailable",
      );
      expect(await state(p, "backfill-duplicate-ts")).toEqual({
        phase: "requests",
        last_event_id: null,
        scanned_count: "0",
      });
      expect(
        (
          await p.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_decisions WHERE product_id='backfill-duplicate-ts'",
          )
        ).rows[0].n,
      ).toBe(0);
      for (const [scope, events] of [
        ["backfill-orphan-ts", [terminal]],
        ["backfill-mismatch-ts", [request, { ...terminal, permission: "other" }]],
        ["backfill-ambiguous-ts", [request, terminal, { ...terminal, eventId: "other-terminal" }]],
      ] as const) {
        await seed(p, scope, events);
        await port(p, scope).backfillDecisionPage(100);
        await port(p, scope).backfillDecisionPage(100);
        await expect(port(p, scope).backfillDecisionPage(100)).rejects.toThrow("audit_unavailable");
        expect(await state(p, scope)).toEqual({
          phase: "outcomes",
          last_event_id: null,
          scanned_count: "0",
        });
      }
      expect(
        (
          await p.query(
            "SELECT resolution FROM heimdall.audit_decisions WHERE product_id='backfill-ambiguous-ts'",
          )
        ).rows[0].resolution,
      ).toBe("pending");
      await seed(p, "backfill-corrupt-ts");
      await p.query(
        "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('b',64),'hex') WHERE product_id='backfill-corrupt-ts'",
      );
      await expect(port(p, "backfill-corrupt-ts").backfillDecisionPage(100)).rejects.toThrow(
        "audit_unavailable",
      );
      expect(await state(p, "backfill-corrupt-ts")).toEqual({
        phase: "requests",
        last_event_id: null,
        scanned_count: "0",
      });
      const ro = new Pool({
        connectionString: url,
        options: "-c default_transaction_read_only=on",
      });
      try {
        await expect(port(ro, "backfill-ts").backfillDecisionPage(1)).rejects.toThrow(
          "unavailable",
        );
      } finally {
        await ro.end();
      }
      await p.query("UPDATE heimdall.schema_metadata SET state='ready'");
      try {
        await expect(port(p, "backfill-ts").backfillDecisionPage(1)).rejects.toThrow("unavailable");
      } finally {
        await p.query("UPDATE heimdall.schema_metadata SET state='migrating'");
      }
      await expect(port(p, "unregistered").backfillDecisionPage(1)).rejects.toThrow("unavailable");
      const lock = await p.connect();
      try {
        await lock.query("BEGIN");
        await lock.query("SELECT pg_advisory_xact_lock(74162401)");
        await expect(
          port(p, "backfill-ts").backfillDecisionPage(1, AbortSignal.timeout(100)),
        ).rejects.toThrow("deadline_exceeded");
      } finally {
        await lock.query("ROLLBACK");
        lock.release();
      }
      expect(await port(p, "backfill-ts").backfillDecisionPage(1)).toEqual(f.steps.at(-1));
      await seed(p, "backfill-empty-ts", []);
      expect(await port(p, "backfill-empty-ts").backfillDecisionPage(100)).toEqual({
        phase: "outcomes",
        after: null,
        scannedCount: 0,
        expectedCount: 0,
        processed: 0,
      });
      expect(await port(p, "backfill-empty-ts").backfillDecisionPage(100)).toEqual({
        phase: "complete",
        after: null,
        scannedCount: 0,
        expectedCount: 0,
        processed: 0,
      });
      await seed(p, "backfill-drift-ts");
      await port(p, "backfill-drift-ts").backfillDecisionPage(100);
      await port(p, "backfill-drift-ts").backfillDecisionPage(100);
      await port(p, "backfill-drift-ts").append({
        ...f.events[3],
        productId: "backfill-drift-ts",
        eventId: "zz-new",
      });
      await expect(port(p, "backfill-drift-ts").backfillDecisionPage(100)).rejects.toThrow(
        "audit_unavailable",
      );
      expect(await state(p, "backfill-drift-ts")).toEqual({
        phase: "outcomes",
        last_event_id: null,
        scanned_count: "0",
      });
      const padded = Array.from({ length: 100 }, (_, i) => ({
        ...f.events[3],
        eventId: `padded-${String(i).padStart(3, "0")}`,
        target: { type: "document", id: "🚀".repeat(8192) },
      }));
      await seed(p, "backfill-budget-ts", padded);
      let budget = await port(p, "backfill-budget-ts").backfillDecisionPage(100);
      expect(budget.processed).toBeGreaterThan(0);
      expect(budget.processed).toBeLessThan(100);
      for (let i = 0; i < 10 && budget.phase !== "complete"; i++)
        budget = await port(p, "backfill-budget-ts").backfillDecisionPage(100);
      expect(budget).toEqual({
        phase: "complete",
        after: null,
        scannedCount: 100,
        expectedCount: 100,
        processed: 0,
      });
      await seed(p, "backfill-race-ts");
      const results = await Promise.all(
        Array.from({ length: 8 }, () => port(p, "backfill-race-ts").backfillDecisionPage(1)),
      );
      expect(results.reduce((n, r) => n + r.processed, 0)).toBe(7);
      await finish(p, "backfill-race-ts", 8);
    } finally {
      await p.end();
    }
  },
  20000,
);
test.skipIf(!url || !process.env.HEIMDALL_TEST_BACKFILL_PEER)(
  "audit backfill resumes Go checkpoint",
  async () => {
    const p = new Pool({ connectionString: url });
    try {
      await finish(p, "backfill-peer-go", 2);
      expect(
        (
          await p.query(
            "SELECT resolution FROM heimdall.audit_decisions WHERE product_id='backfill-peer-go' ORDER BY decision_id",
          )
        ).rows.map((r) => r.resolution),
      ).toEqual(["completed", "pending"]);
    } finally {
      await p.end();
    }
  },
);
test.skipIf(!url || !process.env.HEIMDALL_TEST_BACKFILL_LOSS)(
  "audit backfill committed response loss",
  async () => {
    const p = new Pool({ connectionString: url }),
      fault = new Pool({ connectionString: process.env.HEIMDALL_TEST_BACKFILL_LOSS });
    try {
      await seed(p, "backfill-loss-ts");
      await expect(port(fault, "backfill-loss-ts").backfillDecisionPage(100)).rejects.toThrow(
        "outcome_unknown",
      );
      expect(await state(p, "backfill-loss-ts")).toEqual({
        phase: "requests",
        last_event_id: "z-request",
        scanned_count: "4",
      });
      expect(
        (
          await p.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_decisions WHERE product_id='backfill-loss-ts'",
          )
        ).rows[0].n,
      ).toBe(2);
      await finish(p, "backfill-loss-ts", 4);
    } finally {
      await fault.end();
      await p.end();
    }
  },
);
