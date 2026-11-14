import { readFileSync } from "node:fs";
import { Pool, type PoolClient } from "pg";
import { expect, test } from "vitest";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import { auditDigest } from "../src/internal/audit-receipts.js";
import { HeimdallError } from "../src/internal/errors.js";
const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/managed-audit-transaction.json", import.meta.url),
    "utf8",
  ),
) as {
  event: Record<string, unknown>;
  writes: string[];
  cases: {
    retainedOnly?: boolean;
    name: string;
    error: string | null;
    managedRows: number;
    auditRows: number;
  }[];
};
const validate = (e: Record<string, unknown>) => {
  validateAuditShape(e);
  validateAuditEvent(e as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
const auditStore = (...args: ConstructorParameters<typeof PostgresAudit>) =>
  process.env.HEIMDALL_TEST_MANAGED_AUDIT_V4 === "1"
    ? PostgresAudit.forRetainedRuntime(...args)
    : new PostgresAudit(...args);
const writes = async (tx: PoolClient, scope: string) => {
  for (const sql of fixture.writes) await tx.query(sql.replaceAll("$scope", scope));
};
async function counts(pool: Pool, scope: string) {
  const result = [];
  for (const table of [
    "heimdall_managed.workspaces",
    "heimdall_managed.operations",
    "heimdall_managed.membership_candidates",
    "heimdall.audit_events",
    "heimdall.audit_deliveries",
  ]) {
    result.push(
      (await pool.query(`SELECT count(*)::int AS n FROM ${table} WHERE product_id=$1`, [scope]))
        .rows[0].n,
    );
  }
  return result;
}
test.each([false, true])(
  "atomic audit validates before mutation callback (retained=%s)",
  async (retained) => {
    const pool = new Pool();
    let invoked = false;
    try {
      const store = retained
        ? PostgresAudit.forRetainedRuntime(pool, "ledger", "production", [], validate)
        : new PostgresAudit(pool, "ledger", "production", [], validate);
      await expect(
        store.appendWith({}, async () => {
          invoked = true;
        }),
      ).rejects.toThrow("invalid_argument");
      await expect(
        store.appendWith(
          fixture.event,
          async () => {
            invoked = true;
          },
          AbortSignal.abort(),
        ),
      ).rejects.toThrow("cancelled");
      expect(invoked).toBe(false);
    } finally {
      await pool.end();
    }
  },
);
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("atomic managed writes and audit outbox commit or roll back together", async () => {
  const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  const pool = new Pool({
    connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
    options: "-c role=heimdall_managed_runtime",
  });
  try {
    await admin.query(
      "ALTER TABLE heimdall.audit_deliveries ADD CONSTRAINT atomic_ts_failure CHECK(sink_id<>'atomic-denied-ts')",
    );
    for (const c of fixture.cases) {
      if (c.retainedOnly && process.env.HEIMDALL_TEST_MANAGED_AUDIT_V4 !== "1") continue;
      const scope = "atomic-ts-" + c.name;
      const event: Record<string, unknown> = {
        ...fixture.event,
        productId: scope,
        eventId: "atomic-event",
      };
      const store = auditStore(
        pool,
        scope,
        "production",
        c.name === "outbox_failure" ? ["a-good", "atomic-denied-ts"] : ["primary", "secondary"],
        validate,
      );
      if (c.name === "audit_conflict") await store.append({ ...event, action: "different" });
      const digest = auditDigest(event);
      const controller = new AbortController();
      const outcome = await store
        .appendWith(
          event,
          async (tx) => {
            await writes(tx, scope);
            event.action = "caller-mutated-after-snapshot";
            if (c.name === "mutation_failure") throw new HeimdallError("conflict");
            if (c.name === "mutation_unique")
              await tx.query(fixture.writes[0]!.replaceAll("$scope", scope));
            if (c.name === "cancel_before_commit") controller.abort();
          },
          controller.signal,
        )
        .then(
          (r) => ({ error: null, digest: r.payloadDigest }),
          (e) => ({ error: e.message, digest: null }),
        );
      expect(outcome.error).toBe(c.error);
      expect(outcome.digest).toBe(c.error === null ? digest : null);
      expect(await counts(admin, scope)).toEqual([
        c.managedRows,
        c.managedRows,
        c.managedRows,
        c.auditRows,
        c.auditRows * 2,
      ]);
    }
  } finally {
    await admin.query(
      "ALTER TABLE heimdall.audit_deliveries DROP CONSTRAINT IF EXISTS atomic_ts_failure",
    );
    await pool.end();
    await admin.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_MANAGED_AUDIT_LOSS)(
  "atomic managed audit committed response loss",
  async () => {
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_MANAGED_AUDIT_LOSS,
      options: "-c role=heimdall_managed_runtime",
    });
    const scope = "atomic-loss-ts";
    const event: Record<string, unknown> = {
      ...fixture.event,
      productId: scope,
      eventId: "atomic-event",
    };
    try {
      const store = auditStore(pool, scope, "production", ["primary", "secondary"], validate);
      await expect(store.appendWith(event, (tx) => writes(tx, scope))).rejects.toThrow(
        "outcome_unknown",
      );
      expect(await counts(direct, scope)).toEqual([1, 1, 1, 1, 2]);
      const recovery = auditStore(direct, scope, "production", ["new-sink"], validate);
      expect((await recovery.append(event)).payloadDigest).toBe(auditDigest(event));
      expect(await counts(direct, scope)).toEqual([1, 1, 1, 1, 2]);
    } finally {
      await pool.end();
      await direct.end();
    }
  },
);
