import { createHash } from "node:crypto";
import { encodeIdentity } from "../src/internal/identity-encoding.js";
import { intentDigests } from "../src/internal/operations.js";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { OperationRecords } from "../src/internal/postgres/operations.js";
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/operation-journal.json", import.meta.url),
    "utf8",
  ),
);
const vectors = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/operation-scan.json", import.meta.url), "utf8"),
) as { cases: { queue: "runnable" | "recovery" | "completion"; now: number; ids: string[] }[] };
const store = (p: Pool, scope: string) =>
  new OperationRecords(p, { ...f.binding, productId: scope }, []);
test("operation scan validates bounds and preserves cancellation", async () => {
  const p = new Pool(),
    s = store(p, "scan");
  try {
    await expect(s.scan("runnable", null, -1, 1)).rejects.toThrow("invalid_argument");
    await expect(s.scan("runnable", "bad/id", 0, 1)).rejects.toThrow("invalid_argument");
    await expect(s.scan("runnable", null, 0, 101)).rejects.toThrow("invalid_argument");
    await expect(s.scan("recovery", null, 0, 1, AbortSignal.abort())).rejects.toThrow("cancelled");
  } finally {
    await p.end();
  }
});
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("operation scan reads Go state without replaying or mutating", async () => {
  const p = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  const s = store(p, "transition-go");
  try {
    const snapshot = () =>
      p.query(
        "SELECT operation_id,state,generation::text,dispatch_count::text FROM heimdall_managed.operations WHERE product_id='transition-go' ORDER BY operation_id",
      );
    const before = (await snapshot()).rows;
    for (const c of vectors.cases) {
      const all = await s.scan(c.queue, null, c.now, 100);
      expect(all.items.map((r) => r.operationId)).toEqual(c.ids);
      expect(all.nextOperationId).toBeNull();
      const ids: string[] = [];
      let after: string | null = null;
      do {
        const page = await s.scan(c.queue, after, c.now, 1);
        ids.push(...page.items.map((r) => r.operationId));
        after = page.nextOperationId;
      } while (after !== null);
      expect(ids).toEqual(c.ids);
    }
    expect((await store(p, "other").scan("recovery", null, 300, 100)).items).toEqual([]);
    expect(
      (
        await new OperationRecords(
          p,
          { ...f.binding, productId: "transition-go", environment: "staging" },
          [],
        ).scan("recovery", null, 300, 100)
      ).items,
    ).toEqual([]);
    await expect(
      new OperationRecords(
        p,
        { ...f.binding, productId: "transition-go", modelId: "wrong" },
        [],
      ).scan("recovery", null, 300, 100),
    ).rejects.toThrow("unavailable");
    expect((await snapshot()).rows).toEqual(before);
    const ro = new Pool({
      connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
      options: "-c default_transaction_read_only=on",
    });
    try {
      await expect(store(ro, "missing").scan("recovery", null, 0, 1)).rejects.toThrow(
        "unavailable",
      );
    } finally {
      await ro.end();
    }
    const lock = await p.connect();
    try {
      await lock.query("BEGIN");
      await lock.query("LOCK TABLE heimdall_managed.operations IN ACCESS EXCLUSIVE MODE");
      await expect(s.scan("recovery", null, 0, 1, AbortSignal.timeout(100))).rejects.toThrow(
        "deadline_exceeded",
      );
    } finally {
      await lock.query("ROLLBACK");
      lock.release();
    }
    expect((await s.scan("recovery", null, 0, 100)).items.map((r) => r.operationId)).toEqual([
      "race",
      "safe-retry",
    ]);
  } finally {
    await p.end();
  }
});

test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("operation scan byte budget retains continuation", async () => {
  const p = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  try {
    const scope = "scan-budget",
      i = { ...f.createIntent, productId: scope },
      raw = JSON.stringify(i),
      payload = Buffer.from(raw + " ".repeat(1048576 - Buffer.byteLength(raw)));
    const hash = createHash("sha256")
      .update(encodeIdentity(i.actor.issuer, i.actor.subject), "ascii")
      .digest();
    for (let n = 0; n < 5; n++) {
      const id = `large-${n}`,
        d = intentDigests(i, id);
      await p.query(
        "INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES($1,'production',$2,'Large','pending',true,100,100)",
        [scope, id],
      );
      await p.query(
        `INSERT INTO heimdall_managed.operations(product_id,environment,operation_id,workspace_id,kind,actor_hash,target_hash,key_digest,fingerprint,intent_payload,store_id,model_id,model_hash,manifest_version,state,dispatch_count,generation,next_attempt_at,created_at,updated_at)
  VALUES($1,'production',$2,$2,'create_workspace',$3,$3,$4,$5,$6,$7,$8,$9,1,'pending',0,1,100,100,100)`,
        [
          scope,
          id,
          hash,
          Buffer.from(d.keyDigest, "hex"),
          Buffer.from(d.fingerprint, "hex"),
          payload,
          f.binding.storeId,
          f.binding.modelId,
          Buffer.from(f.binding.modelHash, "hex"),
        ],
      );
    }
    const s = store(p, scope),
      first = await s.scan("runnable", null, 100, 100);
    expect(first.items.map((r) => r.operationId)).toEqual(["large-0", "large-1", "large-2"]);
    expect(first.nextOperationId).toBe("large-2");
    const last = await s.scan("runnable", first.nextOperationId, 100, 100);
    expect(last.items.map((r) => r.operationId)).toEqual(["large-3", "large-4"]);
    expect(last.nextOperationId).toBeNull();
  } finally {
    await p.end();
  }
});
