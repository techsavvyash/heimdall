import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import type { AuditHoldRequest } from "../src/internal/audit-hold.js";
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-hold-storage.json", import.meta.url),
    "utf8",
  ),
) as {
  snapshot: string;
  profiles: Record<
    string,
    {
      target: Record<string, unknown>;
      group: {
        events: Record<string, unknown>[];
        hold: { request: AuditHoldRequest; administration: Record<string, unknown> };
        corrupt: { request: AuditHoldRequest; administration: Record<string, unknown> };
      };
      loss: Record<string, { request: AuditHoldRequest; administration: Record<string, unknown> }>;
      cases: {
        name: string;
        request: AuditHoldRequest;
        administration: Record<string, unknown>;
        expected: string;
        stable: boolean;
      }[];
    }
  >;
};
test.skipIf(!process.env.HEIMDALL_TEST_HOLD_URL)("atomic retained hold operations", async () => {
  const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_HOLD_URL });
  const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_HOLD_ADMIN });
  const peer = process.env.HEIMDALL_TEST_HOLD_PEER === "1";
  const p = f.profiles[process.env.HEIMDALL_TEST_HOLD_PROFILE ?? (peer ? "go" : "ts")]!;
  const port = PostgresAudit.forRetainedRuntime(
    pool,
    p.target.productId as string,
    "production",
    ["siem"],
    (e) => {
      validateAuditShape(e);
      validateAuditEvent(e as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
    },
  );
  try {
    if (process.env.HEIMDALL_TEST_HOLD_REJECT === "1") {
      const before = (await admin.query(f.snapshot)).rows;
      await assert.rejects(port.applyHold(p.cases[0]!.request, p.cases[0]!.administration), {
        code: "unavailable",
      });
      assert.deepEqual((await admin.query(f.snapshot)).rows, before);
      return;
    }
    if (process.env.HEIMDALL_TEST_HOLD_GROUP === "1") {
      for (const event of p.group.events) await port.append(event);
      await port.applyHold(p.group.hold.request, p.group.hold.administration);
      assert.equal(await port.eligibility("tenant-1", "z-request", 9000000000, 1), "retain_held");
      const saved = (
        await admin.query(
          "SELECT encode(payload_digest,'hex') AS digest FROM heimdall.audit_events WHERE product_id=$1 AND event_id='z-request'",
          [p.target.productId],
        )
      ).rows[0].digest;
      await admin.query(
        "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE product_id=$1 AND event_id='z-request'",
        [p.target.productId],
      );
      try {
        const before = (await admin.query(f.snapshot)).rows;
        await assert.rejects(
          port.applyHold(p.group.corrupt.request, p.group.corrupt.administration),
          { code: "audit_unavailable" },
        );
        assert.deepEqual((await admin.query(f.snapshot)).rows, before);
      } finally {
        await admin.query(
          "UPDATE heimdall.audit_events SET payload_digest=decode($2,'hex') WHERE product_id=$1 AND event_id='z-request'",
          [p.target.productId, saved],
        );
      }
      return;
    }
    const operation = process.env.HEIMDALL_TEST_HOLD_OPERATION;
    if (operation) {
      const c = p.loss[operation]!;
      const before = (await admin.query(f.snapshot)).rows;
      const expected = process.env.HEIMDALL_TEST_HOLD_EXPECT;
      if (expected) {
        await assert.rejects(port.applyHold(c.request, c.administration), { code: expected });
        assert.deepEqual((await admin.query(f.snapshot)).rows, before);
        return;
      }
      if (process.env.HEIMDALL_TEST_HOLD_FAULT === "1") {
        await assert.rejects(port.applyHold(c.request, c.administration), {
          code: "outcome_unknown",
        });
        return;
      }
      const receipt = await port.applyHold(c.request, c.administration);
      assert.deepEqual(await port.resolveReceipt(c.administration), receipt);
      if (process.env.HEIMDALL_TEST_HOLD_RETRY === "1")
        assert.deepEqual((await admin.query(f.snapshot)).rows, before);
      const h = (
        await admin.query(
          "SELECT released_at::text FROM heimdall_retention.event_holds WHERE product_id=$1 AND hold_id=$2",
          [c.request.productId, c.request.holdId],
        )
      ).rows;
      assert.deepEqual(h, [{ released_at: operation === "release" ? "5001" : null }]);
      return;
    }
    if (!peer) await port.append(p.target);
    for (const c of p.cases) {
      if (peer && !c.name.startsWith("exact")) continue;
      const before = (await admin.query(f.snapshot)).rows;
      let status = "receipt";
      try {
        const receipt = await port.applyHold(c.request, c.administration);
        assert.deepEqual(await port.resolveReceipt(c.administration), receipt);
      } catch (e) {
        status = (e as Error).message;
      }
      assert.equal(status, c.expected, c.name);
      if (c.stable || peer) assert.deepEqual((await admin.query(f.snapshot)).rows, before, c.name);
    }
    const first = p.cases[0]!;
    await assert.rejects(port.applyHold(first.request, first.administration, AbortSignal.abort()), {
      code: "cancelled",
    });
    const holds = (
      await admin.query(
        "SELECT hold_id,released_at::text FROM heimdall_retention.event_holds WHERE product_id=$1 ORDER BY hold_id",
        [p.target.productId],
      )
    ).rows;
    assert.deepEqual(holds, [
      { hold_id: "hold-1", released_at: "3001" },
      { hold_id: "retry-1", released_at: null },
    ]);
  } finally {
    await pool.end();
    await admin.end();
  }
});
