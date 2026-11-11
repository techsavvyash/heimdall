import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { recoveryEventId } from "../src/internal/audit-recovery.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import { checkAuditReadiness } from "../src/internal/postgres/readiness.js";
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-capacity-writes.json", import.meta.url),
    "utf8",
  ),
);
const history = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/audit-capacity-backfill.json", import.meta.url),
    "utf8",
  ),
);
const validate = (event: Record<string, unknown>) => {
  validateAuditShape(event);
  validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
test.skipIf(!process.env.HEIMDALL_TEST_CAPACITY_HISTORY_URL)(
  "TS seeds scoped history for deployment accounting",
  async () => {
    const p = new Pool({ connectionString: process.env.HEIMDALL_TEST_CAPACITY_HISTORY_URL });
    try {
      for (const scope of history.scopes.filter((s: { language: string }) => s.language === "ts")) {
        const events = structuredClone(f);
        for (const event of [
          events.request,
          events.terminal,
          events.administration,
          events.ordinary,
        ]) {
          event.productId = scope.productId;
          event.environment = scope.environment;
        }
        events.administration.eventId = recoveryEventId(
          scope.productId,
          scope.environment,
          events.terminal.eventId,
        );
        const a = new PostgresAudit(p, scope.productId, scope.environment, ["siem"], validate);
        await a.appendDecision(events.request);
        if (scope.resolution === "recovered")
          await a.recoverDecision(events.terminal, events.administration, events.cutoff);
        await a.append(events.ordinary);
      }
      expect(
        (await p.query("SELECT count(*)::int AS n FROM heimdall.audit_events")).rows[0].n,
      ).toBe(history.events);
    } finally {
      await p.end();
    }
  },
);
test.skipIf(!process.env.HEIMDALL_TEST_CAPACITY_ACCOUNTED_URL)(
  "complete accounting leaves TS runtime unavailable",
  async () => {
    const p = new Pool({ connectionString: process.env.HEIMDALL_TEST_CAPACITY_ACCOUNTED_URL });
    try {
      await expect(checkAuditReadiness(p)).rejects.toThrow("unavailable");
      const a = new PostgresAudit(p, "ledger", "production", ["siem"], validate);
      await expect(a.appendCapacity(f.request)).rejects.toThrow("audit_unavailable");
      expect(
        (
          await p.query(
            "SELECT used_events::int AS used,reserved_events::int AS reserved,state FROM heimdall_capacity.policy",
          )
        ).rows[0],
      ).toEqual({ used: history.events, reserved: history.reservedEvents, state: "preparing" });
    } finally {
      await p.end();
    }
  },
);

test.skipIf(!process.env.HEIMDALL_TEST_CAPACITY_RECOVER_HISTORY_URL)(
  "TS recovers an accounted Go request",
  async () => {
    const p = new Pool({
      connectionString: process.env.HEIMDALL_TEST_CAPACITY_RECOVER_HISTORY_URL,
    });
    try {
      const events = structuredClone(f);
      for (const event of [events.terminal, events.administration]) {
        event.productId = "support";
        event.environment = "staging";
      }
      events.administration.eventId = recoveryEventId(
        "support",
        "staging",
        events.terminal.eventId,
      );
      const a = new PostgresAudit(p, "support", "staging", ["siem"], validate);
      const receipt = await a.recoverCapacity(
        events.terminal,
        events.administration,
        events.cutoff,
      );
      expect(
        await a.recoverCapacity(events.terminal, events.administration, events.cutoff),
      ).toEqual(receipt);
      expect(
        (
          await p.query(
            "SELECT used_events::int AS used,reserved_events::int AS reserved FROM heimdall_capacity.policy",
          )
        ).rows[0],
      ).toEqual({ used: 15, reserved: 0 });
    } finally {
      await p.end();
    }
  },
);
