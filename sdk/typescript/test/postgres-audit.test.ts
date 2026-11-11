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
