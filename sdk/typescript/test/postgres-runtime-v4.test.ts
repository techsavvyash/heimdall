import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { createPostgresStorage } from "../src/postgres.js";
import { checkManagedAuditReadiness } from "../src/internal/postgres/readiness.js";
const root = new URL("../../../contracts/fixtures/", import.meta.url);
function events(environment: string): Record<string, unknown>[] {
  return JSON.parse(readFileSync(new URL("audit-backfill.json", root), "utf8")).events.map(
    (event: Record<string, unknown>) => ({ ...event, environment }),
  );
}
function storage(pool: Pool, environment: string) {
  return createPostgresStorage(pool, { productId: "ledger", environment, auditSinks: ["siem"] });
}
const signal = new AbortController().signal;
test("managed v2 readiness preserves pre-aborted cancellation", async () => {
  const pool = new Pool();
  try {
    await expect(checkManagedAuditReadiness(pool, AbortSignal.abort())).rejects.toThrow(
      "cancelled",
    );
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_FACTORY_URL)(
  "public v4 factory gates writes and persists decision slots",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_FACTORY_URL });
    const port = storage(pool, "factory-ts");
    try {
      await port.ready(signal);
      const fixture = events("factory-ts");
      for (const i of [0, 1, 2, 3, 0, 1]) await port.appendAudit(fixture[i]!, signal);
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS count FROM heimdall.audit_decisions WHERE environment='factory-ts'",
          )
        ).rows[0].count,
      ).toBe(2);
      await expect(port.appendAudit({ ...fixture[1], eventId: "loser" }, signal)).rejects.toThrow(
        "conflict",
      );
      await expect(
        port.appendAudit({ ...fixture[1], eventId: "unknown", outcome: "outcome_unknown" }, signal),
      ).rejects.toThrow("invalid_argument");
      await expect(
        port.appendAudit({ ...fixture[1], eventId: "orphan", decisionId: "missing" }, signal),
      ).rejects.toThrow("audit_unavailable");
      await expect(
        port.appendAudit(
          { ...fixture[0], eventId: "broken-request", decisionId: "broken" },
          signal,
        ),
      ).rejects.toThrow("unavailable");
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS count FROM heimdall.audit_events WHERE environment='factory-ts'",
          )
        ).rows[0].count,
      ).toBe(4);
      for (const statement of [
        "UPDATE heimdall.schema_metadata SET state='migrating'",
        "DELETE FROM heimdall.audit_decisions",
        "DELETE FROM heimdall_retention.event_identities",
        "DELETE FROM heimdall_retention.decision_identities",
        "UPDATE heimdall_retention.capacity SET row_limit=row_limit+1",
        "DELETE FROM heimdall_capacity.event_charges",
        "DELETE FROM heimdall_capacity.reservations",
        "UPDATE heimdall_capacity.policy SET event_limit=event_limit+1",
        "UPDATE heimdall_capacity.upgrade SET event_limit=event_limit+1",
        "UPDATE heimdall.audit_events SET recorded_at=recorded_at",
        "CREATE TABLE heimdall.forbidden(id int)",
      ])
        await expect(pool.query(statement)).rejects.toThrow("permission denied");
      for (const key of [
        "HEIMDALL_TEST_FACTORY_V1",
        "HEIMDALL_TEST_FACTORY_V2",
        "HEIMDALL_TEST_FACTORY_V3",
        "HEIMDALL_TEST_FACTORY_MIGRATING",
      ]) {
        const other = new Pool({ connectionString: process.env[key] });
        try {
          const blocked = storage(other, "factory-ts");
          await expect(blocked.ready(signal)).rejects.toThrow("unavailable");
          await expect(blocked.appendAudit(fixture[3]!, signal)).rejects.toThrow("unavailable");
        } finally {
          await other.end();
        }
      }
      const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_FACTORY_ADMIN, max: 1 });
      try {
        await admin.query("BEGIN; SELECT pg_advisory_xact_lock(74162401)");
        await expect(port.appendAudit(fixture[3]!, AbortSignal.timeout(100))).rejects.toThrow(
          "deadline_exceeded",
        );
        await admin.query("ROLLBACK");
        await admin.query(
          "BEGIN; SELECT pg_advisory_xact_lock(74162401); UPDATE heimdall.schema_metadata SET state='migrating'; COMMIT",
        );
        await expect(port.appendAudit(fixture[3]!, signal)).rejects.toThrow("unavailable");
        await admin.query(
          "BEGIN; SELECT pg_advisory_xact_lock(74162401); UPDATE heimdall.schema_metadata SET state='ready'; COMMIT",
        );
      } finally {
        await admin.query("ROLLBACK");
        await admin.end();
      }
      await port.appendAudit(fixture[3]!, signal);
      const peer = storage(pool, "factory-go");
      for (const event of events("factory-go")) await peer.appendAudit(event, signal);
    } finally {
      await pool.end();
    }
  },
  15000,
);
test.skipIf(!process.env.HEIMDALL_TEST_MANAGED_V2_ADMIN)(
  "managed readiness observes audit v2 and managed v1 coherently",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_V2_ADMIN, max: 1 });
    const cases = JSON.parse(
      readFileSync(new URL("managed-audit-readiness.json", root), "utf8"),
    ) as { name: string; sql: string[]; ready: boolean }[];
    try {
      for (const c of cases) {
        await pool.query("BEGIN");
        try {
          for (const sql of c.sql) await pool.query(sql);
          const result = await checkManagedAuditReadiness(pool).then(
            () => true,
            () => false,
          );
          expect({ name: c.name, result }).toEqual({ name: c.name, result: c.ready });
        } finally {
          await pool.query("ROLLBACK");
        }
      }
      const readonly = new Pool({
        connectionString: process.env.HEIMDALL_TEST_MANAGED_V2_ADMIN,
        options: "-c default_transaction_read_only=on",
      });
      try {
        await expect(checkManagedAuditReadiness(readonly)).rejects.toThrow("unavailable");
      } finally {
        await readonly.end();
      }
    } finally {
      await pool.end();
    }
  },
);
test.skipIf(!process.env.HEIMDALL_TEST_FACTORY_LOSS)(
  "public v4 request COMMIT loss recovers event and slot",
  async () => {
    const faulty = new Pool({ connectionString: process.env.HEIMDALL_TEST_FACTORY_LOSS });
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_FACTORY_URL });
    const event = events("factory-loss-ts")[0]!;
    try {
      await expect(storage(faulty, "factory-loss-ts").appendAudit(event, signal)).rejects.toThrow(
        "outcome_unknown",
      );
      expect(
        (
          await direct.query(
            "SELECT count(*)::int AS count FROM heimdall.audit_decisions WHERE environment='factory-loss-ts'",
          )
        ).rows[0].count,
      ).toBe(1);
      await storage(direct, "factory-loss-ts").appendAudit(event, signal);
    } finally {
      await faulty.end();
      await direct.end();
    }
  },
);
