import { AuditedDecision } from "../src/internal/audited-decision.js";
import { auditDigest } from "../src/internal/audit-receipts.js";
import { readFileSync } from "node:fs";
import { Ajv2020 } from "ajv/dist/2020.js";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import { HeimdallError } from "../src/internal/errors.js";
const root = new URL("../../../contracts/", import.meta.url);
const fixtures = JSON.parse(readFileSync(new URL("fixtures/audit-digests-v1.json", root), "utf8"));
const shape = new Ajv2020({ strict: true }).compile(
  JSON.parse(readFileSync(new URL("audit-event.schema.json", root), "utf8")),
);
const validate = (event: Record<string, unknown>) => {
  if (!shape(event)) throw new HeimdallError("invalid_argument");
  validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
test("audit rejects invalid envelope before acquiring a connection", async () => {
  const pool = new Pool();
  const store = new PostgresAudit(pool, "ledger", "production", [], validate);
  try {
    await expect(store.append({ eventId: "x" })).rejects.toThrow("invalid_argument");
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "atomic audit persistence",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const runtime = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: `-c role=${process.env.HEIMDALL_TEST_AUDIT_ROLE}`,
    });
    const store = new PostgresAudit(
      runtime,
      "ledger",
      "production",
      ["primary", "secondary"],
      validate,
    );
    const event = fixtures[0].event;
    try {
      const unicode = JSON.parse(
        readFileSync(new URL("fixtures/audit-storage-unicode.json", root), "utf8"),
      );
      expect((await store.append(unicode)).eventId).toBe("unicode-nul-shared");
      const receipt = await store.append(event);
      expect(receipt.payloadDigest).toBe(fixtures[0].digest);
      expect(await store.append(fixtures[1].event)).toEqual(receipt);
      const race = { ...event, eventId: "ts-audit-race" };
      const receipts = await Promise.all(Array.from({ length: 8 }, () => store.append(race)));
      expect(new Set(receipts.map((r) => r.payloadDigest)).size).toBe(1);
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE event_id='ts-audit-race'",
          )
        ).rows[0].n,
      ).toBe(2);
      await expect(store.append({ ...race, action: "different" })).rejects.toThrow("conflict");
      await expect(store.append({ ...event, productId: "support" })).rejects.toThrow(
        "invalid_argument",
      );
      await expect(
        store.append({ ...event, eventId: "ts-cancelled" }, AbortSignal.abort()),
      ).rejects.toThrow("cancelled");
      await pool.query(
        "ALTER TABLE heimdall.audit_deliveries ADD CONSTRAINT ts_test_failure CHECK(sink_id<>'z-ts-blocked')",
      );
      try {
        const failing = new PostgresAudit(
          runtime,
          "ledger",
          "production",
          ["a-good", "z-ts-blocked"],
          validate,
        );
        await expect(failing.append({ ...event, eventId: "ts-audit-rollback" })).rejects.toThrow(
          "unavailable",
        );
        expect(
          (
            await pool.query(
              "SELECT count(*)::int AS n FROM heimdall.audit_events WHERE event_id='ts-audit-rollback'",
            )
          ).rows[0].n,
        ).toBe(0);
        expect(
          (
            await pool.query(
              "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE event_id='ts-audit-rollback'",
            )
          ).rows[0].n,
        ).toBe(0);
      } finally {
        await pool.query("ALTER TABLE heimdall.audit_deliveries DROP CONSTRAINT ts_test_failure");
      }
      await pool.query(
        "UPDATE heimdall.audit_events SET event=jsonb_set(event,'{action}','\"tampered\"') WHERE event_id='ts-audit-race'",
      );
      await expect(store.append(race)).rejects.toThrow("audit_unavailable");
    } finally {
      await runtime.end();
      await pool.end();
    }
  },
  15000,
);
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !process.env.HEIMDALL_TEST_READ_PEER)(
  "reads Go audit receipt without changing original sink routing",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    try {
      const store = new PostgresAudit(pool, "ledger", "production", ["new-sink"], validate);
      const receipt = await store.append({ ...fixtures[0].event, eventId: "go-audit-shared" });
      expect(receipt.eventId).toBe("go-audit-shared");
      expect(
        (
          await pool.query(
            "SELECT sink_id FROM heimdall.audit_deliveries WHERE event_id='go-audit-shared' ORDER BY sink_id",
          )
        ).rows,
      ).toEqual([{ sink_id: "primary" }, { sink_id: "secondary" }]);
    } finally {
      await pool.end();
    }
  },
);

test.skipIf(!process.env.HEIMDALL_TEST_AUDIT_COMMIT_LOSS)(
  "audit committed response loss",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_AUDIT_COMMIT_LOSS });
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const event = { ...fixtures[0].event, eventId: "ts-audit-lost-commit" };
    try {
      const store = new PostgresAudit(
        pool,
        "ledger",
        "production",
        ["primary", "secondary"],
        validate,
      );
      await expect(store.append(event)).rejects.toThrow("outcome_unknown");
      expect(
        (
          await direct.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE event_id=$1",
            [event.eventId],
          )
        ).rows[0].n,
      ).toBe(2);
      const recovered = new PostgresAudit(direct, "ledger", "production", ["new-sink"], validate);
      expect((await recovered.append(event)).eventId).toBe(event.eventId);
      expect(
        (
          await direct.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_deliveries WHERE event_id=$1",
            [event.eventId],
          )
        ).rows[0].n,
      ).toBe(2);
    } finally {
      await pool.end();
      await direct.end();
    }
  },
  15000,
);

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "delivery acknowledgement matrix and atomicity",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const deliveryPool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: "-c role=heimdall_audit_delivery",
    });
    const writer = new PostgresAudit(
      pool,
      "ledger",
      "production",
      ["siem", "other-sink"],
      validate,
    );
    const store = new PostgresAudit(deliveryPool, "ledger", "production", [], validate);
    type Delivery = { productId: string; environment: string; sinkId: string; eventIds: string[] };
    const cases = JSON.parse(
      readFileSync(new URL("fixtures/audit-delivery-v1.json", root), "utf8"),
    ) as { name: string; batch: Delivery; receipt: Delivery; valid: boolean }[];
    try {
      for (const [index, item] of cases.entries()) {
        const prefix = `ts-ack-${index}-`;
        const ids = [prefix + "e1", prefix + "e2"];
        for (const eventId of ids) await writer.append({ ...fixtures[0].event, eventId });
        const batch = { ...item.batch, eventIds: item.batch.eventIds.map((id) => prefix + id) };
        const receipt = {
          ...item.receipt,
          eventIds: item.receipt.eventIds.map((id) => prefix + id),
        };
        const result = await store.acknowledge(batch, receipt, 1000).then(
          () => true,
          () => false,
        );
        expect({ name: item.name, result }).toEqual({ name: item.name, result: item.valid });
        const rows = await pool.query(
          "SELECT event_id,sink_id,state FROM heimdall.audit_deliveries WHERE event_id=ANY($1::text[]) ORDER BY event_id,sink_id",
          [ids],
        );
        expect(rows.rows).toEqual(
          ids.flatMap((event_id) => [
            { event_id, sink_id: "other-sink", state: "pending" },
            {
              event_id,
              sink_id: "siem",
              state: item.valid && receipt.eventIds.includes(event_id) ? "delivered" : "pending",
            },
          ]),
        );
      }
      const ids = ["ts-ack-atomic-1", "ts-ack-atomic-2"];
      for (const eventId of ids) await writer.append({ ...fixtures[0].event, eventId });
      const batch = {
        productId: "ledger",
        environment: "production",
        sinkId: "siem",
        eventIds: ids,
      };
      await pool.query(
        "UPDATE heimdall.audit_deliveries SET state='quarantined' WHERE event_id=$1 AND sink_id='siem'",
        [ids[1]],
      );
      await expect(store.acknowledge(batch, batch, 2000)).rejects.toThrow("unavailable");
      expect(
        (
          await pool.query(
            "SELECT state FROM heimdall.audit_deliveries WHERE event_id=$1 AND sink_id='siem'",
            [ids[0]],
          )
        ).rows[0].state,
      ).toBe("pending");
      const missing = { ...batch, eventIds: [ids[0]!, "missing-route"] };
      await expect(store.acknowledge(missing, missing, 2000)).rejects.toThrow("unavailable");
      await pool.query(
        "UPDATE heimdall.audit_deliveries SET state='pending' WHERE event_id=$1 AND sink_id='siem'",
        [ids[1]],
      );
      await Promise.all(Array.from({ length: 8 }, () => store.acknowledge(batch, batch, 3000)));
      await store.acknowledge(batch, batch, 4000);
      expect(
        (
          await pool.query(
            "SELECT delivered_at::text FROM heimdall.audit_deliveries WHERE event_id=ANY($1::text[]) AND sink_id='siem' ORDER BY event_id",
            [ids],
          )
        ).rows,
      ).toEqual([{ delivered_at: "3000" }, { delivered_at: "3000" }]);
    } finally {
      await deliveryPool.end();
      await pool.end();
    }
  },
  20000,
);

test.skipIf(!process.env.HEIMDALL_TEST_ACK_COMMIT_LOSS)(
  "acknowledgement committed response loss",
  async () => {
    const fault = new Pool({ connectionString: process.env.HEIMDALL_TEST_ACK_COMMIT_LOSS });
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const writer = new PostgresAudit(direct, "ledger", "production", ["siem"], validate);
    const store = new PostgresAudit(fault, "ledger", "production", [], validate);
    const ids = ["ts-ack-loss-1", "ts-ack-loss-2"];
    const batch = { productId: "ledger", environment: "production", sinkId: "siem", eventIds: ids };
    const receipt = { ...batch, eventIds: [ids[0]!] };
    try {
      for (const eventId of ids) await writer.append({ ...fixtures[0].event, eventId });
      await expect(store.acknowledge(batch, receipt, 1234)).rejects.toThrow("outcome_unknown");
      const before = await direct.query(
        "SELECT event_id,state,delivered_at::text FROM heimdall.audit_deliveries WHERE event_id=ANY($1::text[]) ORDER BY event_id",
        [ids],
      );
      expect(before.rows).toEqual([
        { event_id: ids[0], state: "delivered", delivered_at: "1234" },
        { event_id: ids[1], state: "pending", delivered_at: null },
      ]);
      await writer.acknowledge(batch, receipt, 2345);
      expect(
        (
          await direct.query(
            "SELECT event_id,state,delivered_at::text FROM heimdall.audit_deliveries WHERE event_id=ANY($1::text[]) ORDER BY event_id",
            [ids],
          )
        ).rows,
      ).toEqual(before.rows);
    } finally {
      await fault.end();
      await direct.end();
    }
  },
  15000,
);

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "pending selection scope time corruption and budget",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const matrix = JSON.parse(
      readFileSync(new URL("fixtures/audit-pending.json", root), "utf8"),
    ) as {
      now: number;
      records: {
        id: string;
        due: number;
        attempts?: number;
        state?: string;
        product?: string;
        environment?: string;
        sink?: string;
        corruption?: string;
        tenantId?: string;
      }[];
      expected: { id: string; attempts: number; valid: boolean }[];
    };
    const product = "ledger-pending-ts";
    try {
      for (const row of matrix.records) {
        const scope = row.product ? "support-pending-ts" : product;
        const environment = row.environment ?? "production";
        const sink = row.sink ?? "pending";
        const event = {
          ...fixtures[0].event,
          productId: scope,
          environment,
          eventId: "ts-pending-" + row.id,
          tenantId: row.tenantId ?? fixtures[0].event.tenantId,
        };
        await new PostgresAudit(pool, scope, environment, [sink], validate).append(event);
        await pool.query(
          "UPDATE heimdall.audit_deliveries SET next_attempt_at=$2,attempts=$3,state=$4,delivered_at=CASE WHEN $4='delivered' THEN 100 ELSE NULL END WHERE event_id=$1",
          [event.eventId, row.due, row.attempts ?? 0, row.state ?? "pending"],
        );
        if (row.corruption === "digest")
          await pool.query(
            "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE event_id=$1",
            [event.eventId],
          );
        if (row.corruption === "payload-scope") {
          const wrong = { ...event, productId: "support-pending-ts" };
          await pool.query(
            "UPDATE heimdall.audit_events SET event=jsonb_set(event,'{payload}',to_jsonb($2::text)),payload_digest=$3 WHERE event_id=$1",
            [
              event.eventId,
              Buffer.from(JSON.stringify(wrong)).toString("base64url"),
              Buffer.from(auditDigest(wrong), "hex"),
            ],
          );
        }
      }
      const store = new PostgresAudit(pool, product, "production", [], validate);
      const selected = await store.pending("pending", matrix.now, 100);
      expect(
        selected.map((item) => ({
          id: item.eventId.replace("ts-pending-", ""),
          attempts: item.attempts,
          valid: item.event !== null,
        })),
      ).toEqual(matrix.expected);
      expect((await store.pending("pending", matrix.now, 2)).map((item) => item.eventId)).toEqual([
        "ts-pending-a",
        "ts-pending-b",
      ]);
      expect(await store.pending("pending", matrix.now, 100)).toEqual(selected);
      await expect(store.pending("pending", matrix.now, 0)).rejects.toThrow("invalid_argument");
      await expect(store.pending("pending", matrix.now, 100, AbortSignal.abort())).rejects.toThrow(
        "cancelled",
      );
      const budget = new PostgresAudit(pool, "budget-ts", "production", ["budget"], validate);
      for (let i = 0; i < 6; i++)
        await budget.append({
          ...fixtures[0].event,
          productId: "budget-ts",
          eventId: `budget-ts-${i}`,
        });
      await pool.query(
        "UPDATE heimdall.audit_events SET event=jsonb_set(event,'{payload}',to_jsonb(repeat('a',1000000))) WHERE product_id='budget-ts'",
      );
      const limited = await budget.pending("budget", 100, 100);
      expect(limited).toHaveLength(4);
      expect(limited.every((item) => item.event === null)).toBe(true);
      const shared = new PostgresAudit(pool, "ledger", "production", ["pending-shared"], validate);
      await shared.append({ ...fixtures[0].event, eventId: "pending-ts-shared" });
    } finally {
      await pool.end();
    }
  },
  20000,
);
test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !process.env.HEIMDALL_TEST_READ_PEER)(
  "pending reader decodes both language writers",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    try {
      const store = new PostgresAudit(pool, "ledger", "production", [], validate);
      const items = await store.pending("pending-shared", 100, 100);
      expect(items.map((item) => item.event?.eventId)).toEqual([
        "pending-go-shared",
        "pending-ts-shared",
      ]);
    } finally {
      await pool.end();
    }
  },
);

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "delivery attempt transitions and poison progress",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const deliveryPool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: "-c role=heimdall_audit_delivery",
    });
    const writer = new PostgresAudit(pool, "retry-ts", "production", ["retry", "other"], validate);
    const store = new PostgresAudit(deliveryPool, "retry-ts", "production", [], validate);
    const event = (eventId: string) => ({ ...fixtures[0].event, productId: "retry-ts", eventId });
    const cases = JSON.parse(
      readFileSync(new URL("fixtures/audit-transitions.json", root), "utf8"),
    ) as {
      operation: string;
      attempt: number;
      now: number;
      retryAt: number;
      result: number | boolean | null;
      storedAttempts: number;
      storedDue: number;
      storedState: string;
    }[];
    try {
      await writer.append(event("retry-ts-main"));
      for (const step of cases) {
        let result: number | boolean | null;
        if (step.operation === "begin")
          result = await store.beginAttempt(
            "retry",
            "retry-ts-main",
            step.attempt,
            step.now,
            step.retryAt,
          );
        else if (step.operation === "defer")
          result = await store.deferAttempt("retry", "retry-ts-main", step.attempt, step.retryAt);
        else result = await store.quarantine("retry", "retry-ts-main", step.attempt);
        expect(result).toBe(step.result);
        expect(
          (
            await pool.query(
              "SELECT attempts,next_attempt_at::text,state FROM heimdall.audit_deliveries WHERE product_id='retry-ts' AND event_id='retry-ts-main' AND sink_id='retry'",
            )
          ).rows[0],
        ).toEqual({
          attempts: step.storedAttempts,
          next_attempt_at: String(step.storedDue),
          state: step.storedState,
        });
      }
      expect(
        (
          await pool.query(
            "SELECT attempts,state FROM heimdall.audit_deliveries WHERE product_id='retry-ts' AND event_id='retry-ts-main' AND sink_id='other'",
          )
        ).rows[0],
      ).toEqual({ attempts: 0, state: "pending" });
      expect(
        await new PostgresAudit(deliveryPool, "support", "production", [], validate).beginAttempt(
          "retry",
          "retry-ts-main",
          0,
          100,
          200,
        ),
      ).toBeNull();
      expect(await store.beginAttempt("missing", "retry-ts-main", 0, 100, 200)).toBeNull();
      await writer.append(event("retry-ts-race"));
      const claims = await Promise.all(
        Array.from({ length: 8 }, () => store.beginAttempt("retry", "retry-ts-race", 0, 100, 200)),
      );
      expect(claims.filter((value) => value !== null)).toEqual([1]);
      const batch = {
        productId: "retry-ts",
        environment: "production",
        sinkId: "retry",
        eventIds: ["retry-ts-race"],
      };
      await store.acknowledge(batch, batch, 150);
      expect(await store.deferAttempt("retry", "retry-ts-race", 1, 999)).toBe(false);
      expect(await store.quarantine("retry", "retry-ts-race", 1)).toBe(false);
      expect(await store.beginAttempt("retry", "retry-ts-race", 1, 999, 1000)).toBeNull();
      await expect(store.beginAttempt("retry", "unused", 2147483647, 100, 200)).rejects.toThrow(
        "invalid_argument",
      );
      await writer.append(event("retry-ts-0-invalid"));
      await writer.append(event("retry-ts-1-healthy"));
      await pool.query(
        "UPDATE heimdall.audit_events SET payload_digest=decode(repeat('00',32),'hex') WHERE event_id='retry-ts-0-invalid'",
      );
      expect((await writer.pending("retry", 100, 1))[0]?.event).toBeNull();
      expect(await store.quarantine("retry", "retry-ts-0-invalid", 0)).toBe(true);
      expect((await writer.pending("retry", 100, 1))[0]?.event?.eventId).toBe("retry-ts-1-healthy");
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_events WHERE event_id='retry-ts-0-invalid'",
          )
        ).rows[0].n,
      ).toBe(1);
      await expect(
        store.beginAttempt("retry", "retry-ts-1-healthy", 0, 100, 200, AbortSignal.abort()),
      ).rejects.toThrow("cancelled");
    } finally {
      await deliveryPool.end();
      await pool.end();
    }
  },
  20000,
);

test.skipIf(!process.env.HEIMDALL_TEST_TRANSITION_LOSS)(
  "delivery transition committed response loss",
  async () => {
    const fault = new Pool({ connectionString: process.env.HEIMDALL_TEST_TRANSITION_LOSS });
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const operation = process.env.HEIMDALL_TEST_TRANSITION_KIND!;
    const id = `transition-ts-loss-${operation}`;
    const writer = new PostgresAudit(direct, "ledger", "production", ["transition-loss"], validate);
    const store = new PostgresAudit(fault, "ledger", "production", [], validate);
    const invoke = (target: PostgresAudit) => {
      if (operation === "begin") return target.beginAttempt("transition-loss", id, 0, 100, 200);
      if (operation === "defer") return target.deferAttempt("transition-loss", id, 1, 300);
      return target.quarantine("transition-loss", id, 1);
    };
    try {
      await writer.append({ ...fixtures[0].event, eventId: id });
      if (operation !== "begin") await writer.beginAttempt("transition-loss", id, 0, 100, 200);
      await expect(invoke(store)).rejects.toThrow("outcome_unknown");
      const before = await direct.query(
        "SELECT attempts,next_attempt_at::text,state FROM heimdall.audit_deliveries WHERE event_id=$1",
        [id],
      );
      expect(before.rows).toEqual([
        {
          attempts: 1,
          next_attempt_at: operation === "defer" ? "300" : "200",
          state: operation === "quarantine" ? "quarantined" : "pending",
        },
      ]);
      expect(await invoke(writer)).toBe(operation === "begin" ? null : true);
      expect(
        (
          await direct.query(
            "SELECT attempts,next_attempt_at::text,state FROM heimdall.audit_deliveries WHERE event_id=$1",
            [id],
          )
        ).rows,
      ).toEqual(before.rows);
    } finally {
      await fault.end();
      await direct.end();
    }
  },
  15000,
);

test.skipIf(!process.env.HEIMDALL_TEST_DATABASE_URL || !!process.env.HEIMDALL_TEST_READ_PEER)(
  "audited decision uses committed PostgreSQL receipts",
  async () => {
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_DATABASE_URL,
      options: `-c role=${process.env.HEIMDALL_TEST_AUDIT_ROLE}`,
    });
    try {
      const store = new PostgresAudit(pool, "ledger", "auditflow-ts", ["primary"], validate);
      const ids: string[] = [];
      const runner = new AuditedDecision(
        "ledger",
        "auditflow-ts",
        async (e, s) => {
          const r = await store.append(e, s);
          ids.push(r.eventId);
          return r;
        },
        validate,
      );
      for (const allowed of [true, false]) {
        const result = await runner.run(
          { ...fixtures[0].event, environment: "auditflow-ts" },
          async () => {
            const rows = await pool.query(
              "SELECT event_id FROM heimdall.audit_events WHERE product_id='ledger' AND environment='auditflow-ts' AND event_id=$1",
              [ids.at(-1)],
            );
            expect(rows.rowCount).toBe(1);
            return allowed;
          },
        );
        expect(result).toBe(allowed);
        expect(
          (
            await pool.query(
              "SELECT event_id FROM heimdall.audit_deliveries WHERE product_id='ledger' AND environment='auditflow-ts' AND event_id=$1",
              [ids.at(-1)],
            )
          ).rowCount,
        ).toBe(1);
      }
    } finally {
      await pool.end();
    }
  },
);
test.skipIf(!process.env.HEIMDALL_TEST_DECISION_COMMIT_LOSS)(
  "decision audit committed response loss",
  async () => {
    const fault = new Pool({ connectionString: process.env.HEIMDALL_TEST_DECISION_COMMIT_LOSS });
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_DATABASE_URL });
    const stage = Number(process.env.HEIMDALL_TEST_DECISION_LOSS_STAGE);
    const events: Record<string, unknown>[] = [];
    let evaluated = false;
    try {
      const normal = new PostgresAudit(direct, "ledger", "decision-loss-ts", ["primary"], validate);
      const broken = new PostgresAudit(fault, "ledger", "decision-loss-ts", ["primary"], validate);
      const runner = new AuditedDecision(
        "ledger",
        "decision-loss-ts",
        async (e, s) => {
          events.push(structuredClone(e));
          return (events.length === stage ? broken : normal).append(e, s);
        },
        validate,
      );
      await expect(
        runner.run({ ...fixtures[0].event, environment: "decision-loss-ts" }, async () => {
          evaluated = true;
          return true;
        }),
      ).rejects.toThrow("audit_unavailable");
      expect(evaluated).toBe(stage === 2);
      expect(events.length).toBe(stage);
      for (const event of events) {
        expect(
          (
            await direct.query("SELECT event_id FROM heimdall.audit_events WHERE event_id=$1", [
              event.eventId,
            ])
          ).rowCount,
        ).toBe(1);
        expect((await normal.append(event)).payloadDigest).toBe(auditDigest(event));
      }
    } finally {
      await fault.end();
      await direct.end();
    }
  },
);
