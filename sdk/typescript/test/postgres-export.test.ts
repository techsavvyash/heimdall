import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { ManagedExport, decodeExportRow } from "../src/internal/postgres/export.js";
const f = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/managed-export.json", import.meta.url), "utf8"),
);
for (const c of f.cases.filter((c: { valid: boolean }) => c.valid))
  test(`export decoder ${c.name}`, () => {
    const item = decodeExportRow(c.row, c.kind, "export", "test", "workspace");
    expect(item.workspaceId).toBe("workspace");
    expect(item.createdAt).toBe(1);
    expect(item.updatedAt).toBe(2);
    expect(Object.keys(item).sort()).toEqual(Object.keys(c.row).sort());
  });
for (const c of f.cases.filter((c: { valid: boolean }) => !c.valid))
  test(`export decoder ${c.name}`, () => {
    expect(() => decodeExportRow(c.row, c.kind, "export", "test", "workspace")).toThrow(
      "unavailable",
    );
  });
test("export validates bounds and cancellation without I/O", async () => {
  const p = new Pool();
  const s = new ManagedExport(p, "export", "test");
  try {
    for (const limit of [0, 101, 1.5])
      await expect(s.scan("workspaces", null, null, limit)).rejects.toThrow("invalid_argument");
    await expect(s.scan("operations", null, null, 1)).rejects.toThrow("invalid_argument");
    await expect(s.scan("workspaces", "../bad", null, 1)).rejects.toThrow("invalid_argument");
    await expect(s.scan("workspaces", null, "", 1)).rejects.toThrow("invalid_argument");
    await expect(s.scan("workspaces", null, null, 1, AbortSignal.abort())).rejects.toThrow(
      "cancelled",
    );
  } finally {
    await p.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL)(
  "sanitized export pages with column-only privileges and cross-language rows",
  async () => {
    const url = process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL;
    const p = new Pool({ connectionString: url });
    const restricted = new Pool({
      connectionString: url,
      options: "-c role=heimdall_export_reader",
    });
    const peer = !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER;
    const scope = peer ? "export-go" : "export-ts";
    try {
      if (!peer) for (const sql of f.seed) await p.query(sql.replaceAll("$scope", scope));
      const s = new ManagedExport(restricted, scope, "test");
      for (const kind of ["workspaces", "operations"] as const) {
        const workspace = kind === "operations" ? "w-000" : null;
        const first = JSON.parse(await s.scan(kind, workspace, null, 100));
        expect(first.items).toHaveLength(100);
        expect(first.nextId).toBe(kind === "workspaces" ? "w-099" : "op-099");
        const last = JSON.parse(await s.scan(kind, workspace, first.nextId, 100));
        expect(last.items).toHaveLength(
          (kind === "workspaces" ? f.workspaceCount : f.operationCount) - 100,
        );
        expect(last.nextId).toBeNull();
        const raw = JSON.stringify([first, last]);
        for (const forbidden of [
          "PRIVATE",
          "name",
          "actorHash",
          "targetHash",
          "keyDigest",
          "fingerprint",
          "intent",
          "envelope",
        ])
          expect(raw).not.toContain(`"${forbidden}`);
        expect(raw).not.toContain("PRIVATE");
      }
      expect(JSON.parse(await s.scan("operations", "w-000", "op-100", 1)).items[0].state).toBe(
        "uncertain",
      );
      expect(JSON.parse(await s.scan("workspaces", "w-002", null, 100)).items).toHaveLength(1);
      expect(JSON.parse(await s.scan("operations", "w-001", null, 100)).items).toEqual([]);
      expect(
        JSON.parse(
          await new ManagedExport(restricted, "absent-export", "test").scan(
            "workspaces",
            null,
            null,
            100,
          ),
        ).items,
      ).toEqual([]);
      await expect(
        restricted.query("SELECT name FROM heimdall_managed.workspaces LIMIT 1"),
      ).rejects.toThrow("permission denied");
      await expect(
        restricted.query("SELECT intent_payload FROM heimdall_managed.operations LIMIT 1"),
      ).rejects.toThrow("permission denied");
      const ro = new Pool({
        connectionString: url,
        options: "-c default_transaction_read_only=on",
      });
      try {
        await expect(
          new ManagedExport(ro, scope, "test").scan("workspaces", null, null, 1),
        ).rejects.toThrow("unavailable");
      } finally {
        await ro.end();
      }
      const lock = await p.connect();
      try {
        await lock.query("BEGIN");
        await lock.query("LOCK TABLE heimdall_managed.workspaces IN ACCESS EXCLUSIVE MODE");
        await expect(s.scan("workspaces", null, null, 1, AbortSignal.timeout(100))).rejects.toThrow(
          "deadline_exceeded",
        );
      } finally {
        await lock.query("ROLLBACK");
        lock.release();
      }
      expect(JSON.parse(await s.scan("workspaces", null, null, 1)).items).toHaveLength(1);
    } finally {
      await restricted.end();
      await p.end();
    }
  },
  15000,
);
