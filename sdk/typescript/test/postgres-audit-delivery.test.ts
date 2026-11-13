import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { createAuditDelivery, createPostgresStorage } from "../src/postgres.js";

test.skipIf(!process.env.HEIMDALL_TEST_AUDIT_DELIVERY_DB)(
  "exports a durable batch and preserves pending IDs across failure",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_AUDIT_DELIVERY_DB });
    const scope = { productId: "ledger", environment: "delivery-ts", sinkId: "siem" };
    let mode = "outage";
    const accepted: string[] = [];
    const server = createServer(async (request, response) => {
      if (request.headers.authorization !== "Bearer test-token") {
        response.writeHead(401).end();
        return;
      }
      const chunks: Buffer[] = [];
      for await (const chunk of request) chunks.push(chunk);
      const { events, eventIds, ...binding } = JSON.parse(Buffer.concat(chunks).toString());
      if (mode === "outage") {
        response.writeHead(503).end();
        return;
      }
      if (mode === "poison" && eventIds.includes("z-request")) {
        response.writeHead(422).end();
        return;
      }
      const ids = mode === "partial" ? eventIds.slice(0, 1) : eventIds;
      accepted.push(...ids);
      response.writeHead(200, { "content-type": "application/json" }).end(
        JSON.stringify({
          ...binding,
          eventIds: mode === "invalid" ? ["unknown"] : ids,
        }),
      );
      if (events.length !== eventIds.length) throw new Error("invalid delivery fixture");
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      const address = server.address() as { port: number };
      const delivery = createAuditDelivery(pool, {
        ...scope,
        endpoint: "http://127.0.0.1:" + address.port,
        token: "test-token",
        transportProfile: "loopback",
      });
      const store = createPostgresStorage(pool, { ...scope, auditSinks: [scope.sinkId] });
      const fixture = JSON.parse(
        readFileSync(
          new URL("../../../contracts/fixtures/audit-backfill.json", import.meta.url),
          "utf8",
        ),
      );
      const signal = AbortSignal.timeout(15000);
      for (const event of fixture.events.slice(0, 2)) {
        await store.appendAudit({ ...event, environment: scope.environment }, signal);
      }
      const due = () =>
        pool.query(
          "UPDATE heimdall.audit_deliveries SET next_attempt_at=0 WHERE environment=$1 AND state='pending'",
          [scope.environment],
        );
      await expect(delivery.deliver(10, signal)).rejects.toThrow("unavailable");
      expect(await delivery.deliver(10, signal)).toEqual({
        attempted: 0,
        delivered: 0,
        quarantined: 0,
      });
      await due();
      mode = "invalid";
      await expect(delivery.deliver(10, signal)).rejects.toThrow("unavailable");
      await due();
      mode = "poison";
      expect(await delivery.deliver(10, signal)).toEqual({
        attempted: 2,
        delivered: 1,
        quarantined: 1,
      });
      expect(await delivery.requeue("z-request", 3, signal)).toBe(true);
      mode = "partial";
      expect(await delivery.deliver(10, signal)).toEqual({
        attempted: 1,
        delivered: 1,
        quarantined: 0,
      });
      const { rows } = await pool.query(
        "SELECT event_id,state,attempts FROM heimdall.audit_deliveries WHERE environment=$1 ORDER BY event_id",
        [scope.environment],
      );
      expect(rows).toEqual([
        { event_id: "a-terminal", state: "delivered", attempts: 3 },
        { event_id: "z-request", state: "delivered", attempts: 5 },
      ]);
      expect(new Set(accepted)).toEqual(new Set(["a-terminal", "z-request"]));
    } finally {
      server.close();
      await pool.end();
    }
  },
  20000,
);
