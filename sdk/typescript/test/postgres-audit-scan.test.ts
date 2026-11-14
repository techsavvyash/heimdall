import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
const base = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/audit-backfill.json", import.meta.url), "utf8"),
);
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-decision-scan.json", import.meta.url),
    "utf8",
  ),
);
const url = process.env.HEIMDALL_TEST_SCAN_DATABASE_URL;
const validate = (e: Record<string, unknown>) => {
  validateAuditShape(e);
  validateAuditEvent(e as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
const port = (p: Pool, scope: string) =>
  new PostgresAudit(p, scope, "production", ["siem"], validate);
const event = (scope: string, id: string, at: number) => ({
  ...base.events[0],
  productId: scope,
  eventId: `request-${id}`,
  decisionId: id,
  recordedAt: at,
  occurredAt: at,
});
const terminal = (e: Record<string, unknown>) => ({
  ...e,
  eventId: `terminal-${e.decisionId}`,
  eventType: "heimdall.authorization.completed",
  outcome: "allow",
});
async function seed(p: Pool, scope: string) {
  for (const r of f.requests) {
    const e = event(scope, r.decisionId, r.requestedAt);
    await port(p, scope).appendDecision(e);
    if (r.complete) await port(p, scope).appendDecision(terminal(e));
  }
}
async function pages(p: Pool, scope: string) {
  let after: Parameters<PostgresAudit["scanPendingDecisions"]>[1] = null;
  for (const ids of f.pages) {
    const page = await port(p, scope).scanPendingDecisions(f.cutoff, after, f.limit);
    expect(page.items.map((x) => x.decisionId)).toEqual(ids);
    for (const item of page.items)
      expect(item.request).toEqual(event(scope, item.decisionId, item.requestedAt));
    after = page.next;
  }
  expect(after).toBeNull();
}
test("pending decision scan input and cancellation", async () => {
  const p = new Pool();
  try {
    const a = port(p, "scan-input");
    for (const limit of [0, 101, 1.5])
      await expect(a.scanPendingDecisions(2000, null, limit)).rejects.toThrow("invalid_argument");
    for (const cutoff of [-1, 1.5, Number.MAX_SAFE_INTEGER + 1])
      await expect(a.scanPendingDecisions(cutoff, null, 1)).rejects.toThrow("invalid_argument");
    for (const patch of f.badCursorPatches)
      await expect(
        a.scanPendingDecisions(
          2000,
          {
            productId: "scan-input",
            environment: "production",
            cutoff: 2000,
            requestedAt: 1001,
            decisionId: "A",
            ...patch,
          },
          1,
        ),
      ).rejects.toThrow("invalid_argument");
    await expect(a.scanPendingDecisions(2000, null, 1, AbortSignal.abort())).rejects.toThrow(
      "cancelled",
    );
  } finally {
    await p.end();
  }
});
test.skipIf(!url || !!process.env.HEIMDALL_TEST_SCAN_PEER)(
  "pending decision primary pages and refusal",
  async () => {
    const p = new Pool({ connectionString: url });
    try {
      await seed(p, "scan-ts");
      await pages(p, "scan-ts");
      await seed(p, "scan-peer-ts");
      expect(await port(p, "scan-empty").scanPendingDecisions(2000, null, 1)).toEqual({
        items: [],
        next: null,
      });
      const a = port(p, "scan-ts"),
        first = await a.scanPendingDecisions(2000, null, 2);
      await a.appendDecision(terminal(event("scan-ts", "later", 1002)));
      expect(await a.scanPendingDecisions(2000, first.next, 2)).toEqual({ items: [], next: null });
      for (const [scope, sql] of [
        [
          "scan-corrupt-ts",
          "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('b',64),'hex') WHERE product_id=$1",
        ],
      ] as const) {
        await port(p, scope).appendDecision(event(scope, "bad", 1001));
        await p.query(sql, [scope]);
        await expect(port(p, scope).scanPendingDecisions(2000, null, 1)).rejects.toThrow(
          "audit_unavailable",
        );
      }
      await port(p, "scan-oversize-ts").appendDecision(event("scan-oversize-ts", "bad", 1001));
      await expect(
        p.query(
          "UPDATE heimdall.audit_events SET event=event || jsonb_build_object('padding',repeat('x',5000000)) WHERE product_id='scan-oversize-ts'",
        ),
      ).rejects.toMatchObject({ code: "23514" });
      expect(
        (await port(p, "scan-oversize-ts").scanPendingDecisions(2000, null, 1)).items,
      ).toHaveLength(1);
      await port(p, "scan-binding-ts").append(event("scan-binding-ts", "bad", 1001));
      await p.query(
        "INSERT INTO heimdall.audit_decisions VALUES('scan-binding-ts','production','bad','request-bad',1000,'pending',NULL,NULL)",
      );
      await expect(port(p, "scan-binding-ts").scanPendingDecisions(2000, null, 1)).rejects.toThrow(
        "audit_unavailable",
      );
      const ro = new Pool({
        connectionString: url,
        options: "-c default_transaction_read_only=on",
      });
      try {
        await expect(port(ro, "scan-empty").scanPendingDecisions(2000, null, 1)).rejects.toThrow(
          "unavailable",
        );
      } finally {
        await ro.end();
      }
      const lock = await p.connect();
      try {
        await lock.query("BEGIN; LOCK heimdall.audit_decisions IN ACCESS EXCLUSIVE MODE");
        await expect(
          a.scanPendingDecisions(2000, null, 1, AbortSignal.timeout(100)),
        ).rejects.toThrow("deadline_exceeded");
      } finally {
        await lock.query("ROLLBACK");
        lock.release();
      }
      await pages(p, "scan-peer-ts");
      const large = port(p, "scan-byte-ts");
      for (let i = 0; i < 100; i++)
        await large.appendDecision({
          ...event("scan-byte-ts", `d${String(i).padStart(3, "0")}`, 1001),
          target: { type: "invoice", id: "🚀".repeat(8192) },
        });
      let page = await large.scanPendingDecisions(2000, null, 100);
      expect(page.items.length).toBeGreaterThan(0);
      expect(page.items.length).toBeLessThan(100);
      expect(page.next).not.toBeNull();
      const seen = page.items.map((x) => x.decisionId);
      while (page.next) {
        page = await large.scanPendingDecisions(2000, page.next, 100);
        seen.push(...page.items.map((x) => x.decisionId));
        if (seen.length > 100) throw new Error("repeated page");
      }
      expect(new Set(seen).size).toBe(100);
    } finally {
      await p.end();
    }
  },
  20000,
);
test.skipIf(!url || !process.env.HEIMDALL_TEST_SCAN_PEER)(
  "pending decision TS reads Go pages",
  async () => {
    const p = new Pool({ connectionString: url });
    try {
      await expect(pages(p, "scan-peer-go")).resolves.toBeUndefined();
    } finally {
      await p.end();
    }
  },
);
