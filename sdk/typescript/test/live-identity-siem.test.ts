import { Pool } from "pg";
import { expect, test } from "vitest";
import {
  createAuditDelivery,
  createIdentityCollector,
  deriveIdentityEventCoverage,
} from "../src/postgres.js";

test.skipIf(!process.env.HEIMDALL_TEST_IDENTITY_SIEM)(
  "collects and exports a live identity event",
  async () => {
    const config = JSON.parse(process.env.HEIMDALL_TEST_IDENTITY_SIEM!);
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_AUDIT_DELIVERY_DB });
    const scope = { productId: "ledger", environment: "live-siem-ts" };
    try {
      const signal = AbortSignal.timeout(25000);
      if (config.stage === "collect" || config.stage === "scheduled") {
        const collector = createIdentityCollector(pool, {
          ...scope,
          issuer: config.issuer,
          organizationId: config.organizationId,
          token: config.providerToken,
          tenantId: "acme",
          auditSinks: ["siem"],
          startMicros: BigInt(config.startMicros),
          aggregates: [{ type: "user", id: config.subject }],
        });
        if (config.stage === "scheduled") {
          const before = await collector.checkpoint(signal);
          const coverage = deriveIdentityEventCoverage(config.policy);
          const collected = await collector.collect(coverage, signal);
          if (
            !before ||
            collected.nextMicros <= before.nextMicros ||
            collected.recorded !== config.expectedEvents
          )
            throw new Error("Scheduled checkpoint did not advance with the expected event count");
          return;
        }
        const collected = await collector.collect(
          {
            retainedFromMicros: BigInt(config.startMicros) - 1n,
            visibleBeforeMicros: BigInt(config.startMicros) + 1n,
            validUntilMs: Date.now() + 25000,
          },
          signal,
        );
        if (collected.recorded !== 1 || collected.nextMicros !== BigInt(config.startMicros) + 1n)
          throw new Error("Unexpected live collection result");
      }
      const delivery = createAuditDelivery(pool, {
        ...scope,
        sinkId: "siem",
        tenantId: "acme",
        endpoint: config.endpoint,
        token: config.sinkToken,
        transportProfile: "loopback",
      });
      const result = await delivery.deliver(10, signal).then(
        (value) => ({ delivered: value.delivered }),
        (error) => ({ error: error.code }),
      );
      expect(result).toEqual(
        config.stage === "retry" ? { delivered: 1 } : { error: "unavailable" },
      );
    } finally {
      await pool.end();
    }
  },
);
