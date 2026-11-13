import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { createIdentityCollector } from "../src/postgres.js";

test.skipIf(!process.env.HEIMDALL_TEST_AUDIT_DELIVERY_DB)(
  "retains mapped source events and advances once",
  async () => {
    const fixture = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/identity-collector.json", import.meta.url),
        "utf8",
      ),
    );
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_AUDIT_DELIVERY_DB });
    let outage = true;
    let reads = 0;
    const server = createServer((_request, response) => {
      reads++;
      response
        .writeHead(outage ? 503 : 200, { "content-type": "application/json" })
        .end(JSON.stringify({ events: fixture.events }));
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      const { port } = server.address() as { port: number };
      const options = {
        productId: "ledger",
        environment: "collector-ts",
        tenantId: "tenant-a",
        issuer: "http://127.0.0.1:" + port,
        organizationId: "org",
        token: "token",
        transportProfile: "loopback" as const,
        auditSinks: ["siem"],
        startMicros: BigInt(fixture.nextMicros),
        aggregates: [{ type: "user", id: "assigned" }],
      };
      const signal = AbortSignal.timeout(15000);
      const coverage = {
        retainedFromMicros: 0n,
        visibleBeforeMicros: BigInt(fixture.untilMicros),
        validUntilMs: Date.now() + 15000,
      };
      const collector = createIdentityCollector(pool, options);
      await expect(
        collector.collect({ ...coverage, retainedFromMicros: options.startMicros }, signal),
      ).rejects.toMatchObject({ code: "unavailable" });
      expect(reads).toBe(0);
      await expect(collector.collect(coverage, signal)).rejects.toMatchObject({
        code: "unavailable",
      });
      expect(
        (
          await pool.query(
            "SELECT next_micros FROM heimdall_directory.event_checkpoints WHERE environment='collector-ts'",
          )
        ).rowCount,
      ).toBe(0);
      outage = false;
      const failing = createIdentityCollector(pool, { ...options, auditSinks: ["inject-failure"] });
      await expect(failing.collect(coverage, signal)).rejects.toMatchObject({
        code: "unavailable",
      });
      expect(
        (
          await pool.query(
            "SELECT next_micros FROM heimdall_directory.event_checkpoints WHERE environment='collector-ts'",
          )
        ).rowCount,
      ).toBe(0);
      expect(
        (
          await pool.query(
            "SELECT event_id FROM heimdall.audit_events WHERE environment='collector-ts'",
          )
        ).rowCount,
      ).toBe(0);
      await expect(collector.collect(coverage, signal)).resolves.toEqual({
        nextMicros: coverage.visibleBeforeMicros,
        recorded: 1,
      });
      const restarted = createIdentityCollector(pool, options);
      await expect(restarted.collect(coverage, signal)).resolves.toEqual({
        nextMicros: coverage.visibleBeforeMicros,
        recorded: 0,
      });
      expect(reads).toBe(3);
      await expect(
        createIdentityCollector(pool, {
          ...options,
          aggregates: [{ type: "user", id: "unassigned" }],
        }).collect(coverage, signal),
      ).rejects.toMatchObject({ code: "conflict" });
      const stored = await pool.query(
        "SELECT event FROM heimdall.audit_events WHERE environment='collector-ts'",
      );
      expect(stored.rowCount).toBe(1);
      const payload = JSON.parse(Buffer.from(stored.rows[0].event.payload, "base64url").toString());
      expect(payload).toMatchObject({
        tenantId: "tenant-a",
        source: "zitadel",
        target: { id: "assigned" },
        attributes: { sequence: "18446744073709551615" },
      });
      expect(JSON.stringify(payload)).not.toContain("discard");
      expect(
        (
          await pool.query(
            "SELECT event_id FROM heimdall.audit_deliveries WHERE environment='collector-ts' AND sink_id='siem'",
          )
        ).rowCount,
      ).toBe(1);
    } finally {
      server.close();
      await pool.end();
    }
  },
);
