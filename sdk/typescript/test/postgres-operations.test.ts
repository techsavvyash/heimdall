import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { OperationRecords, decodeOperation } from "../src/internal/postgres/operations.js";
import type { Intent } from "../src/internal/operations.js";
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/operation-journal.json", import.meta.url),
    "utf8",
  ),
) as {
  binding: ConstructorParameters<typeof OperationRecords>[1];
  actorHash: string;
  cases: { name: string; row: Record<string, unknown>; valid: boolean }[];
  createIntent: Intent;
  key: string;
  allocation: Parameters<OperationRecords["create"]>[2];
};
for (const c of f.cases.filter((c) => c.valid))
  test(`journal decoder ${c.name}`, () => {
    expect(decodeOperation(c.row, f.binding, f.actorHash).state).toBe(c.row.state);
  });
for (const c of f.cases.filter((c) => !c.valid))
  test(`journal rejects ${c.name}`, () => {
    expect(() => decodeOperation(c.row, f.binding, f.actorHash)).toThrow("unavailable");
  });
const intent = (scope: string) => JSON.stringify({ ...f.createIntent, productId: scope });
const allocation = (id: string) => ({
  ...f.allocation,
  workspaceId: "workspace-" + id,
  operationId: "operation-" + id,
  eventId: "event-" + id,
});
const store = (pool: Pool, scope: string, sinks = ["primary", "secondary"]) =>
  new OperationRecords(pool, { ...f.binding, productId: scope }, sinks);
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
test("operation store validates before I/O and preserves cancellation", async () => {
  const pool = new Pool();
  const s = store(pool, "creation-ts");
  try {
    expect(() => new OperationRecords(pool, { ...f.binding, modelHash: "invalid" }, [])).toThrow(
      "invalid_argument",
    );
    await expect(s.get("bad\n", f.createIntent.actor)).rejects.toThrow("invalid_argument");
    await expect(s.create("{}", f.key, allocation("invalid"))).rejects.toThrow("invalid_argument");
    await expect(s.create(intent("other"), f.key, allocation("invalid"))).rejects.toThrow(
      "invalid_argument",
    );
    await expect(
      s.create(intent("creation-ts"), f.key, allocation("invalid"), AbortSignal.abort()),
    ).rejects.toThrow("cancelled");
    await expect(s.get("missing", f.createIntent.actor, AbortSignal.abort())).rejects.toThrow(
      "cancelled",
    );
  } finally {
    await pool.end();
  }
});
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL ||
    !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER ||
    !!process.env.HEIMDALL_TEST_CREATE_RACE,
)("operation creation is durable idempotent actor scoped and atomic", async () => {
  const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  const pool = new Pool({
    connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
    options: "-c role=heimdall_managed_runtime",
  });
  const scope = "creation-ts",
    s = store(pool, scope);
  try {
    expect(await s.get("missing", f.createIntent.actor)).toBeNull();
    const first = await s.create(intent(scope), f.key, allocation("first"));
    expect(first).toMatchObject({
      state: "pending",
      generation: 1,
      dispatchCount: 0,
      operationId: "operation-first",
      workspaceId: "workspace-first",
      intent: { name: f.createIntent.name, actor: f.createIntent.actor },
    });
    expect(await counts(admin, scope)).toEqual([1, 1, 0, 1, 2]);
    expect(
      (
        await admin.query(
          "SELECT lifecycle,mutation_blocked FROM heimdall_managed.workspaces WHERE product_id=$1",
          [scope],
        )
      ).rows,
    ).toEqual([{ lifecycle: "pending", mutation_blocked: true }]);
    expect(await s.create(intent(scope), f.key, allocation("retry"))).toEqual(first);
    await expect(
      s.create(
        JSON.stringify({ ...f.createIntent, productId: scope, name: "Changed" }),
        f.key,
        allocation("changed"),
      ),
    ).rejects.toThrow("conflict");
    expect(await counts(admin, scope)).toEqual([1, 1, 0, 1, 2]);
    expect(
      await s.get(first.operationId, { ...f.createIntent.actor, subject: "other" }),
    ).toBeNull();
    expect(await store(pool, "other").get(first.operationId, f.createIntent.actor)).toBeNull();
    await expect(
      new OperationRecords(pool, { ...f.binding, productId: scope, modelId: "different" }, []).get(
        first.operationId,
        f.createIntent.actor,
      ),
    ).rejects.toThrow("unavailable");
    await admin.query(
      "UPDATE heimdall_managed.operations SET state='cancelled',generation=2,updated_at=102 WHERE product_id=$1",
      [scope],
    );
    expect((await s.create(intent(scope), f.key, allocation("terminal"))).state).toBe("cancelled");
    expect(await counts(admin, scope)).toEqual([1, 1, 0, 1, 2]);
    const uncertain = await s.create(intent(scope), "uncertain-key", allocation("uncertain"));
    await admin.query(
      "UPDATE heimdall_managed.operations SET state='uncertain',dispatch_count=1,generation=2 WHERE product_id=$1 AND operation_id=$2",
      [scope, uncertain.operationId],
    );
    expect((await s.create(intent(scope), "uncertain-key", allocation("never-replay"))).state).toBe(
      "uncertain",
    );
    await expect(
      s.create(intent(scope), "audit-collision", {
        ...allocation("collision"),
        eventId: "event-first",
      }),
    ).rejects.toThrow("conflict");
    expect(await counts(admin, scope)).toEqual([2, 2, 0, 2, 4]);
    const raceScope = "creation-race-ts",
      race = store(pool, raceScope);
    const results = await Promise.all(
      Array.from({ length: 8 }, (_, i) =>
        race.create(intent(raceScope), f.key, allocation("race-" + i)),
      ),
    );
    expect(new Set(results.map((r) => r.operationId)).size).toBe(1);
    expect(new Set(results.map((r) => r.workspaceId)).size).toBe(1);
    expect(await counts(admin, raceScope)).toEqual([1, 1, 0, 1, 2]);
    const conflictScope = "creation-conflict-ts",
      conflict = store(pool, conflictScope);
    const conflicts = await Promise.allSettled([
      conflict.create(intent(conflictScope), f.key, allocation("a")),
      conflict.create(
        JSON.stringify({ ...f.createIntent, productId: conflictScope, name: "Different" }),
        f.key,
        allocation("b"),
      ),
    ]);
    expect(conflicts.filter((x) => x.status === "fulfilled")).toHaveLength(1);
    expect(conflicts.filter((x) => x.status === "rejected").map((x) => x.reason.code)).toEqual([
      "conflict",
    ]);
    expect(await counts(admin, conflictScope)).toEqual([1, 1, 0, 1, 2]);
    await admin.query(
      "ALTER TABLE heimdall.audit_deliveries ADD CONSTRAINT create_ts_failure CHECK(sink_id<>'z-create-blocked-ts')",
    );
    try {
      await expect(
        store(pool, "creation-fail-ts", ["a-good", "z-create-blocked-ts"]).create(
          intent("creation-fail-ts"),
          f.key,
          allocation("failure"),
        ),
      ).rejects.toThrow("unavailable");
      expect(await counts(admin, "creation-fail-ts")).toEqual([0, 0, 0, 0, 0]);
    } finally {
      await admin.query("ALTER TABLE heimdall.audit_deliveries DROP CONSTRAINT create_ts_failure");
    }
    const readonly = new Pool({
      connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
      options: "-c default_transaction_read_only=on",
    });
    try {
      await expect(store(readonly, scope).get("missing", f.createIntent.actor)).rejects.toThrow(
        "unavailable",
      );
    } finally {
      await readonly.end();
    }
  } finally {
    await pool.end();
    await admin.end();
  }
});
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL ||
    !process.env.HEIMDALL_TEST_MANAGED_READ_PEER ||
    !!process.env.HEIMDALL_TEST_CREATE_RACE,
)("operation reader and retry consume Go records", async () => {
  const pool = new Pool({
    connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
    options: "-c role=heimdall_managed_runtime",
  });
  try {
    const s = store(pool, "creation-go");
    const r = await s.get("operation-first", f.createIntent.actor);
    expect(r?.state).toBe("cancelled");
    expect(r?.intent.actor.subject).toBe(f.createIntent.actor.subject);
    expect(await s.create(intent("creation-go"), f.key, allocation("ts-peer-retry"))).toEqual(r);
    expect(await counts(pool, "creation-go")).toEqual([2, 2, 0, 2, 4]);
  } finally {
    await pool.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_CREATE_LOSS)(
  "operation create committed response loss and retry",
  async () => {
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
    const pool = new Pool({
      connectionString: process.env.HEIMDALL_TEST_CREATE_LOSS,
      options: "-c role=heimdall_managed_runtime",
    });
    const scope = "creation-loss-ts";
    try {
      await expect(
        store(pool, scope).create(intent(scope), f.key, allocation("lost")),
      ).rejects.toMatchObject({ code: "outcome_unknown", operationId: "operation-lost" });
      expect(await counts(direct, scope)).toEqual([1, 1, 0, 1, 2]);
      const r = await store(direct, scope, ["new-sink"]).create(
        intent(scope),
        f.key,
        allocation("retry"),
      );
      expect(r.operationId).toBe("operation-lost");
      expect(r.workspaceId).toBe("workspace-lost");
      expect(await counts(direct, scope)).toEqual([1, 1, 0, 1, 2]);
    } finally {
      await pool.end();
      await direct.end();
    }
  },
);
test.skipIf(!process.env.HEIMDALL_TEST_CREATE_RACE)("cross-language create race", async () => {
  const pool = new Pool({
    connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
    options: "-c role=heimdall_managed_runtime",
  });
  try {
    const r = await store(pool, "creation-cross").create(
      intent("creation-cross"),
      f.key,
      allocation("cross-ts"),
    );
    expect(["operation-cross-ts", "operation-cross-go"]).toContain(r.operationId);
  } finally {
    await pool.end();
  }
});
