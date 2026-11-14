import { ManagedExport } from "../src/internal/postgres/export.js";
import { WorkspaceRecords } from "../src/internal/postgres/workspaces.js";
import { CandidateRecords } from "../src/internal/postgres/candidates.js";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { test, expect } from "vitest";
import { OperationRecords } from "../src/internal/postgres/operations.js";
const read = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const f = read("operation-journal"),
  fixture = read("managed-retained-runtime"),
  repair = read("candidate-repair");
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL ||
    process.env.HEIMDALL_TEST_MANAGED_OPERATIONS_V4 !== "1",
)("retained operation admission covers absent reads empty scans and writes", async () => {
  const connectionString = process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL;
  const admin = new Pool({ connectionString }),
    pool = new Pool({ connectionString, options: "-c role=heimdall_managed_runtime" }),
    readonly = new Pool({ connectionString, options: "-c default_transaction_read_only=on" });
  const binding = { ...f.binding, productId: "admission-ts" };
  const store = OperationRecords.forRetainedRuntime(pool, binding, []);
  const reject = async (s: OperationRecords, source: Pool) => {
    const exporter = ManagedExport.forRetainedRuntime(
      source,
      binding.productId,
      binding.environment,
    );
    for (const kind of ["workspaces", "operations"] as const)
      await expect(exporter.scan(kind, "absent", null, 10)).rejects.toThrow("unavailable");

    await expect(
      WorkspaceRecords.forRetainedRuntime(source, binding.productId, binding.environment).get(
        "absent",
      ),
    ).rejects.toThrow("unavailable");
    await expect(
      CandidateRecords.forRetainedRuntime(source, binding.productId, binding.environment).scan(
        f.createIntent.actor,
        "0",
        10,
      ),
    ).rejects.toThrow("unavailable");
    await expect(s.get("absent", f.createIntent.actor)).rejects.toThrow("unavailable");
    await expect(s.scan("recovery", null, 300, 10)).rejects.toThrow("unavailable");
    await expect(
      s.create(
        JSON.stringify({ ...f.createIntent, productId: binding.productId }),
        "key",
        f.allocation,
      ),
    ).rejects.toThrow("unavailable");
    await expect(
      s.repairCandidates("absent", "1", repair.observations, {
        eventId: "repair",
        requestId: "request",
        policyVersion: "policy",
        occurredAt: 200,
        recordedAt: 200,
      }),
    ).rejects.toThrow("unavailable");
  };
  try {
    for (const c of fixture.cases) {
      await admin.query(c.change);
      try {
        await reject(store, pool);
      } finally {
        await admin.query(c.restore);
      }
    }
    await reject(OperationRecords.forRetainedRuntime(readonly, binding, []), readonly);
    expect(await store.get("absent", f.createIntent.actor)).toBeNull();
    expect(await store.scan("recovery", null, 300, 10)).toEqual({
      items: [],
      nextOperationId: null,
    });
    expect(
      (
        await admin.query(
          "SELECT count(*)::int AS n FROM heimdall_managed.operations WHERE product_id=$1",
          [binding.productId],
        )
      ).rows[0].n,
    ).toBe(0);
  } finally {
    await pool.end();
    await readonly.end();
    await admin.end();
  }
});
