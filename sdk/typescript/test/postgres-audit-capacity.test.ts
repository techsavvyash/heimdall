import { checkAuditReadiness } from "../src/internal/postgres/readiness.js";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-capacity-writes.json", import.meta.url),
    "utf8",
  ),
);
const validate = (event: Record<string, unknown>) => {
  validateAuditShape(event);
  validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
const port = (p: Pool, sinks = ["siem"]) =>
  new PostgresAudit(p, "ledger", "production", sinks, validate);
const url = process.env.HEIMDALL_TEST_CAPACITY_URL;
const mode = process.env.HEIMDALL_TEST_CAPACITY_MODE;
test("capacity input validation and cancellation precede IO", async () => {
  const p = new Pool();
  try {
    const a = port(p);
    await expect(a.appendCapacity(f.terminal)).rejects.toThrow("invalid_argument");
    await expect(a.appendCapacity({})).rejects.toThrow("invalid_argument");
    await expect(
      a.recoverCapacity(f.terminal, { ...f.administration, eventId: "wrong" }, f.cutoff),
    ).rejects.toThrow("invalid_argument");
    await expect(a.appendCapacity(f.request, AbortSignal.abort())).rejects.toThrow("cancelled");
    await expect(
      a.recoverCapacity(f.terminal, f.administration, f.cutoff, AbortSignal.abort()),
    ).rejects.toThrow("cancelled");
  } finally {
    await p.end();
  }
});
test.skipIf(!url)(
  "capacity PostgreSQL transaction scenarios",
  async () => {
    assert.ok(f.scenarios.includes(mode));
    const p = new Pool({ connectionString: url });
    const fault = new Pool({ connectionString: process.env.HEIMDALL_TEST_CAPACITY_LOSS ?? url });
    const a = port(p),
      lost = port(fault);
    const counters = async (used: number, reserved: number) => {
      const row = (
        await p.query(
          "SELECT used_events::int AS used,reserved_events::int AS reserved,used_bytes=(SELECT coalesce(sum(envelope_bytes),0) FROM heimdall_capacity.event_charges) AS bytes,used_deliveries=(SELECT coalesce(sum(delivery_rows),0) FROM heimdall_capacity.event_charges) AS deliveries FROM heimdall_capacity.policy",
        )
      ).rows[0];
      assert.deepEqual(row, { used, reserved, bytes: true, deliveries: true });
      assert.equal(
        (await p.query("SELECT count(*)::int AS n FROM heimdall.audit_events")).rows[0].n,
        used,
      );
    };
    try {
      if (["preparing", "bytes", "deliveries"].includes(mode!)) {
        await assert.rejects(a.appendCapacity(f.request), { message: "audit_unavailable" });
        await counters(0, 0);
        return;
      }
      if (mode === "admission") {
        const attempts = await Promise.allSettled(
          Array.from({ length: 8 }, (_, i) => {
            const productId = i % 2 ? "ledger" : "support";
            const writer = new PostgresAudit(p, productId, "production", ["siem"], validate);
            return writer.appendCapacity({
              ...f.request,
              productId,
              eventId: `request-${i}`,
              decisionId: `decision-${i}`,
            });
          }),
        );
        assert.equal(attempts.filter((x) => x.status === "fulfilled").length, 1);
        assert.deepEqual(
          attempts.filter((x) => x.status === "rejected").map((x) => x.reason.message),
          Array(7).fill("audit_unavailable"),
        );
        await counters(1, 2);
        return;
      }
      if (mode === "legacy") {
        await a.appendDecision(f.request);
        await assert.rejects(a.appendCapacity(f.request), { message: "audit_unavailable" });
        await assert.rejects(a.appendCapacity(f.normal), { message: "audit_unavailable" });
        assert.deepEqual(
          (
            await p.query(
              "SELECT used_events::int AS used,reserved_events::int AS reserved FROM heimdall_capacity.policy",
            )
          ).rows[0],
          { used: 0, reserved: 0 },
        );
        assert.equal(
          (await p.query("SELECT count(*)::int AS n FROM heimdall.audit_events")).rows[0].n,
          1,
        );
        return;
      }
      if (mode === "loss_ordinary") {
        await assert.rejects(lost.appendCapacity(f.ordinary), { message: "outcome_unknown" });
        await a.appendCapacity(f.ordinary);
        await counters(1, 0);
        return;
      }
      if (mode !== "peer_recovery") {
        if (mode === "loss_request")
          await assert.rejects(lost.appendCapacity(f.request), { message: "outcome_unknown" });
        const receipt = await a.appendCapacity(f.request);
        assert.deepEqual(await a.appendCapacity(f.request), receipt);
        await counters(1, 2);
        // Changed sink configuration must preserve original outbox rows on retry.
        assert.deepEqual(await port(p, ["different"]).appendCapacity(f.request), receipt);
        await assert.rejects(a.appendCapacity({ ...f.request, requestId: "changed" }), {
          message: "conflict",
        });
        await assert.rejects(a.appendCapacity(f.ordinary), { message: "audit_unavailable" });
        await counters(1, 2);
      }
      if (mode === "sink_failure") {
        await assert.rejects(a.recoverCapacity(f.terminal, f.administration, f.cutoff), {
          message: "unavailable",
        });
        await counters(1, 2);
        assert.equal(
          (await p.query("SELECT resolution FROM heimdall.audit_decisions")).rows[0].resolution,
          "pending",
        );
        return;
      }
      if (mode === "peer_request") return;
      if (mode === "race") {
        const results = await Promise.allSettled([
          a.appendCapacity(f.normal),
          a.recoverCapacity(f.terminal, f.administration, f.cutoff),
        ]);
        assert.equal(results.filter((x) => x.status === "fulfilled").length, 1);
        assert.deepEqual(
          results.filter((x) => x.status === "rejected").map((x) => x.reason.message),
          ["conflict"],
        );
        await counters(results[0].status === "fulfilled" ? 2 : 3, 0);
        return;
      }
      if (["recovery", "loss_recovery", "peer_recovery"].includes(mode!)) {
        if (mode === "loss_recovery")
          await assert.rejects(lost.recoverCapacity(f.terminal, f.administration, f.cutoff), {
            message: "outcome_unknown",
          });
        const receipt = await a.recoverCapacity(f.terminal, f.administration, f.cutoff);
        assert.deepEqual(await a.recoverCapacity(f.terminal, f.administration, f.cutoff), receipt);
        await assert.rejects(a.appendCapacity(f.normal), { message: "conflict" });
        await assert.rejects(
          a.recoverCapacity(f.terminal, { ...f.administration, requestId: "changed" }, f.cutoff),
          { message: "conflict" },
        );
        await assert.rejects(a.appendCapacity(f.ordinary), { message: "audit_unavailable" });
        await counters(3, 0);
      } else {
        if (mode === "loss_completion")
          await assert.rejects(lost.appendCapacity(f.normal), { message: "outcome_unknown" });
        const receipt = await a.appendCapacity(f.normal);
        assert.deepEqual(await a.appendCapacity(f.normal), receipt);
        await a.appendCapacity(f.request);
        await counters(2, 0);
        await assert.rejects(a.recoverCapacity(f.terminal, f.administration, f.cutoff), {
          message: "conflict",
        });
        await a.appendCapacity(f.ordinary);
        await counters(3, 0);
      }
    } finally {
      await fault.end();
      await p.end();
    }
  },
  20000,
);

test.skipIf(!process.env.HEIMDALL_TEST_CAPACITY_UPGRADE_URL)(
  "offline Go capacity preparation refuses both TS writer paths",
  async () => {
    const p = new Pool({ connectionString: process.env.HEIMDALL_TEST_CAPACITY_UPGRADE_URL });
    try {
      const a = port(p);
      await expect(checkAuditReadiness(p)).rejects.toThrow("unavailable");
      await expect(a.appendRuntime(f.request)).rejects.toThrow("unavailable");
      await expect(a.appendCapacity(f.request)).rejects.toThrow("audit_unavailable");
      expect(
        (
          await p.query(
            "SELECT used_events::int AS used,reserved_events::int AS reserved FROM heimdall_capacity.policy",
          )
        ).rows[0],
      ).toEqual({ used: 1, reserved: 0 });
      expect(
        (await p.query("SELECT count(*)::int AS n FROM heimdall.audit_events")).rows[0].n,
      ).toBe(6);
    } finally {
      await p.end();
    }
  },
);
