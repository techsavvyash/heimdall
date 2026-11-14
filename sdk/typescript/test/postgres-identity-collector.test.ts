import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { createAuditDelivery, createIdentityCollector } from "../src/postgres.js";

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
    let beforeReply: (() => Promise<void>) | undefined;
    const server = createServer(async (_request, response) => {
      reads++;
      const prepare = beforeReply;
      await prepare?.();
      response
        .writeHead(outage ? 503 : 200, { "content-type": "application/json" })
        .end(JSON.stringify({ events: prepare ? [] : fixture.events }));
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
      const changed = createIdentityCollector(pool, {
        ...options,
        aggregates: [{ type: "user", id: "unassigned" }],
      });
      await expect(changed.collect(coverage, signal)).rejects.toMatchObject({ code: "conflict" });
      const before = (await collector.checkpoint(signal))!;
      expect(before).toMatchObject({
        nextMicros: coverage.visibleBeforeMicros,
        mappingRevision: 0,
      });
      await expect(
        changed.activateMapping({ ...before, nextMicros: before.nextMicros - 1n }, signal),
      ).rejects.toMatchObject({ code: "conflict" });
      const after = await changed.activateMapping(before, signal);
      expect(after).toMatchObject({ nextMicros: before.nextMicros, mappingRevision: 1 });
      expect(after.mappingDigest).not.toBe(before.mappingDigest);
      await expect(changed.activateMapping(before, signal)).resolves.toEqual(after);
      await expect(collector.collect(coverage, signal)).rejects.toMatchObject({ code: "conflict" });
      await expect(changed.collect(coverage, signal)).resolves.toEqual({
        nextMicros: before.nextMicros,
        recorded: 0,
      });
      const reverted = await collector.activateMapping(after, signal);
      expect(reverted).toEqual({ ...before, mappingRevision: 2 });
      await expect(changed.activateMapping(before, signal)).rejects.toMatchObject({
        code: "conflict",
      });
      beforeReply = async () => {
        const temporary = await changed.activateMapping(reverted, signal);
        await collector.activateMapping(temporary, signal);
      };
      await expect(
        collector.collect(
          {
            ...coverage,
            visibleBeforeMicros: before.nextMicros + 1n,
          },
          signal,
        ),
      ).rejects.toMatchObject({ code: "conflict" });
      expect(await collector.checkpoint(signal)).toEqual({ ...before, mappingRevision: 4 });
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

test.skipIf(!process.env.HEIMDALL_TEST_COLLECTOR_COMMIT_LOSS)(
  "delivers the original mapped event after the collector loses its commit response",
  async () => {
    const fixture = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/identity-collector.json", import.meta.url),
        "utf8",
      ),
    );
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_AUDIT_DELIVERY_DB });
    const fault = new Pool({
      connectionString: process.env.HEIMDALL_TEST_COLLECTOR_COMMIT_LOSS,
      max: 1,
    });
    let reads = 0;
    const batches: unknown[] = [];
    const server = createServer(async (request, response) => {
      response.setHeader("content-type", "application/json");
      if (request.url === "/admin/v1/events/_search") {
        reads++;
        response.end(JSON.stringify({ events: fixture.events }));
        return;
      }
      if (request.url !== "/audit" || request.headers.authorization !== "Bearer sink-token") {
        response.writeHead(403).end();
        return;
      }
      const chunks: Buffer[] = [];
      for await (const chunk of request) chunks.push(chunk);
      const { events, ...receipt } = JSON.parse(Buffer.concat(chunks).toString());
      batches.push({ ...receipt, events });
      response.end(JSON.stringify(receipt));
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    try {
      const { port } = server.address() as { port: number };
      const issuer = "http://127.0.0.1:" + port;
      const scope = {
        productId: "ledger",
        environment: "collector-loss-ts",
        transportProfile: "loopback" as const,
      };
      const options = {
        ...scope,
        issuer,
        organizationId: "org",
        tenantId: "tenant-a",
        token: "provider-token",
        auditSinks: ["siem"],
        startMicros: BigInt(fixture.nextMicros),
        aggregates: [{ type: "user", id: "assigned" }],
      };
      const coverage = {
        retainedFromMicros: 0n,
        visibleBeforeMicros: BigInt(fixture.untilMicros),
        validUntilMs: Date.now() + 15000,
      };
      const signal = AbortSignal.timeout(15000);
      await expect(
        createIdentityCollector(fault, options).collect(coverage, signal),
      ).rejects.toMatchObject({ code: "outcome_unknown" });
      await expect(
        createIdentityCollector(pool, options).collect(coverage, signal),
      ).resolves.toEqual({
        nextMicros: coverage.visibleBeforeMicros,
        recorded: 0,
      });
      expect(reads).toBe(1);
      const delivery = createAuditDelivery(pool, {
        ...scope,
        sinkId: "siem",
        endpoint: issuer + "/audit",
        token: "sink-token",
      });
      await expect(delivery.deliver(10, signal)).resolves.toMatchObject({
        delivered: 1,
        quarantined: 0,
      });
      await expect(delivery.deliver(10, signal)).resolves.toMatchObject({ delivered: 0 });
      expect(batches).toEqual([
        {
          productId: "ledger",
          environment: "collector-loss-ts",
          sinkId: "siem",
          eventIds: [expect.stringMatching(/^[a-f0-9]{64}$/)],
          events: [
            expect.objectContaining({
              source: "zitadel",
              tenantId: "tenant-a",
              target: { type: "zitadel_aggregate", id: "assigned" },
              attributes: expect.objectContaining({ sequence: "18446744073709551615" }),
            }),
          ],
        },
      ]);
      expect(JSON.stringify(batches)).not.toContain("discard");
    } finally {
      server.close();
      await Promise.all([fault.end(), pool.end()]);
    }
  },
);
