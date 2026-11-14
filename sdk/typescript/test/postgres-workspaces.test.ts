import { workspaceRecords } from "./managed-store.js";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { decodeWorkspace } from "../src/internal/postgres/workspaces.js";
import { workspaceGate } from "../src/internal/workspace-contract.js";
const scope = { productId: "ledger", environment: "test", workspaceId: "acme" };
const cases = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/managed-workspace-read.json", import.meta.url),
    "utf8",
  ),
) as { name: string; row: Record<string, unknown>; expected: string }[];
for (const c of cases.filter((c) => c.expected === "unavailable"))
  test(`workspace decoder ${c.name}`, () => {
    expect(() => decodeWorkspace(c.row, scope)).toThrow("unavailable");
  });
for (const c of cases.filter((c) => c.expected !== "unavailable"))
  test(`workspace decoder ${c.name}`, () => {
    const record = decodeWorkspace(c.row, scope);
    expect(record === null ? "missing" : workspaceGate(record, scope)).toBe(c.expected);
    expect(record?.revision ?? null).toBe(c.row.revision);
    expect(record?.name ?? null).toBe(c.row.name);
  });
test("workspace reader validates inputs and cancels before connection", async () => {
  const pool = new Pool();
  try {
    expect(() => workspaceRecords(pool, "ledger\n", "test")).toThrow("invalid_argument");
    const store = workspaceRecords(pool, "ledger", "test");
    await expect(store.get("acme\n")).rejects.toThrow("invalid_argument");
    await expect(store.get("acme", AbortSignal.abort())).rejects.toThrow("cancelled");
  } finally {
    await pool.end();
  }
});
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("workspace primary reads enforce scope and observe committed lifecycle", async () => {
  const url = process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL;
  const admin = new Pool({ connectionString: url });
  const pool = new Pool({ connectionString: url, options: "-c role=heimdall_managed_status" });
  const readonly = new Pool({
    connectionString: url,
    options: "-c default_transaction_read_only=on",
  });
  const store = workspaceRecords(pool, "read-ts", "test");
  try {
    await admin.query("GRANT SELECT ON heimdall_managed.workspaces TO heimdall_managed_status");
    expect(await store.get("acme")).toBeNull();
    await admin.query(
      "INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES('read-ts','test','acme','Acme 🏢','active',false,0,0)",
    );
    const first = await store.get("acme");
    expect(first?.name).toBe("Acme 🏢");
    expect(
      workspaceGate(first, { productId: "read-ts", environment: "test", workspaceId: "acme" }),
    ).toBe("check_relationships");
    expect(await workspaceRecords(pool, "other", "test").get("acme")).toBeNull();
    expect(await workspaceRecords(pool, "read-ts", "other").get("acme")).toBeNull();
    const tx = await admin.connect();
    try {
      await tx.query(
        "BEGIN; UPDATE heimdall_managed.workspaces SET mutation_blocked=true,updated_at=1 WHERE product_id='read-ts'",
      );
      expect((await store.get("acme"))?.mutationBlocked).toBe(false);
      await tx.query("COMMIT");
    } finally {
      await tx.query("ROLLBACK");
      tx.release();
    }
    const blocked = await store.get("acme");
    expect(blocked?.mutationBlocked).toBe(true);
    expect(blocked?.revision).not.toBe(first?.revision);
    expect(
      workspaceGate(blocked, { productId: "read-ts", environment: "test", workspaceId: "acme" }),
    ).toBe("deny");
    await expect(workspaceRecords(readonly, "read-ts", "test").get("absent")).rejects.toThrow(
      "unavailable",
    );
    const lock = await admin.connect();
    try {
      await lock.query("BEGIN; LOCK TABLE heimdall_managed.workspaces IN ACCESS EXCLUSIVE MODE");
      await expect(store.get("acme", AbortSignal.timeout(100))).rejects.toThrow(
        "deadline_exceeded",
      );
    } finally {
      await lock.query("ROLLBACK");
      lock.release();
    }
    expect((await store.get("acme"))?.mutationBlocked).toBe(true);
    await pool.end();
    await expect(store.get("acme")).rejects.toThrow("unavailable");
  } finally {
    await admin.end();
    if (!pool.ended) await pool.end();
    await readonly.end();
  }
});
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("workspace reader decodes Go writes", async () => {
  const pool = new Pool({
    connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
    options: "-c role=heimdall_managed_status",
  });
  try {
    const r = await workspaceRecords(pool, "read-go", "test").get("acme");
    expect(r?.name).toBe("Acme 🏢");
    expect(r?.mutationBlocked).toBe(true);
    expect(r?.updatedAt).toBe(1);
  } finally {
    await pool.end();
  }
});
