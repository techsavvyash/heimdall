import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { CandidateRecords, decodeCandidatePage } from "../src/internal/postgres/candidates.js";
import { encodeIdentity } from "../src/internal/identity-encoding.js";
const f = JSON.parse(
  readFileSync(new URL("../../../contracts/fixtures/candidate-page.json", import.meta.url), "utf8"),
) as {
  scope: { productId: string; environment: string };
  actor: { issuer: string; subject: string };
  principal: string;
  identityHash: string;
  cases: {
    name: string;
    value: { primary: boolean; records: unknown };
    valid: boolean;
    after: string;
    limit: number;
    positions?: string[];
    nextPosition?: string | null;
  }[];
};
for (const c of f.cases.filter((c) => c.valid))
  test(`candidate page ${c.name}`, () => {
    const page = decodeCandidatePage(
      c.value,
      f.scope,
      f.principal,
      f.identityHash,
      c.after,
      c.limit,
    );
    expect(page.items.map((i) => i.position)).toEqual(c.positions);
    expect(page.nextPosition).toBe(c.nextPosition);
  });
for (const c of f.cases.filter((c) => !c.valid))
  test(`candidate page rejects ${c.name}`, () => {
    expect(() =>
      decodeCandidatePage(c.value, f.scope, f.principal, f.identityHash, c.after, c.limit),
    ).toThrow("unavailable");
  });
test("candidate input validation and cancellation precede I/O", async () => {
  const p = new Pool(),
    s = new CandidateRecords(p, "ledger", "production");
  try {
    expect(() => new CandidateRecords(p, "bad/path", "production")).toThrow("invalid_argument");
    for (const after of ["-1", "01", "1\n", "9223372036854775808"]) {
      await expect(s.scan(f.actor, after, 20)).rejects.toThrow("invalid_argument");
    }
    for (const limit of [0, 101, 1.5]) {
      await expect(s.scan(f.actor, "0", limit)).rejects.toThrow("invalid_argument");
    }
    await expect(s.scan(f.actor, "0", 20, AbortSignal.abort())).rejects.toThrow("cancelled");
    expect(encodeIdentity(f.actor.issuer, f.actor.subject)).toBe(f.principal);
    expect(createHash("sha256").update(f.principal, "ascii").digest("hex")).toBe(f.identityHash);
  } finally {
    await p.end();
  }
});
const enabled = !!process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL;
const peer = !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER;
async function seed(p: Pool, scope: string) {
  // Administrative test data includes inactive/blocked candidates and identical names.
  await p.query(
    `INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at)
  SELECT $1,'production','w-'||lpad(n::text,3,'0'),'Same name',CASE WHEN n=102 THEN 'pending' WHEN n=103 THEN 'suspended' WHEN n=104 THEN 'tombstoned' ELSE 'active' END,n=105,100,100 FROM generate_series(1,105) n`,
    [scope],
  );
  await p.query(
    `INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at)
  SELECT product_id,environment,workspace_id,$2,$3,100 FROM heimdall_managed.workspaces WHERE product_id=$1 ORDER BY workspace_id`,
    [scope, Buffer.from(f.identityHash, "hex"), f.principal],
  );
}
test.skipIf(!enabled || peer)(
  "candidate primary keyset pages stay scoped and reflect lifecycle changes",
  async () => {
    const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
    const p = new Pool({
      connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
      options: "-c role=heimdall_managed_runtime",
    });
    const s = new CandidateRecords(p, "scan-ts", "production");
    try {
      await seed(admin, "scan-ts");
      const first = await s.scan(f.actor, "0", 100);
      expect(first.items).toHaveLength(100);
      expect(first.nextPosition).toBe(first.items[99]!.position);
      const last = await s.scan(f.actor, first.nextPosition!, 100);
      expect(last.items.map((x) => x.workspace.workspaceId)).toEqual(["w-101"]);
      expect(last.nextPosition).toBeNull();
      expect(first.items.map((x) => x.workspace.workspaceId)).toEqual(
        Array.from({ length: 100 }, (_, i) => `w-${String(i + 1).padStart(3, "0")}`),
      );
      expect(
        (await new CandidateRecords(p, "other", "production").scan(f.actor, "0", 100)).items,
      ).toEqual([]);
      expect(
        (await new CandidateRecords(p, "scan-ts", "staging").scan(f.actor, "0", 100)).items,
      ).toEqual([]);
      expect((await s.scan({ ...f.actor, subject: "other" }, "0", 100)).items).toEqual([]);
      // Partial consumption resumes at the consumed item, not the batch tail.
      const partial = await s.scan(f.actor, first.items[0]!.position, 1);
      expect(partial.items[0]!.workspace.workspaceId).toBe("w-002");
      await admin.query(
        "UPDATE heimdall_managed.workspaces SET mutation_blocked=true WHERE product_id='scan-ts' AND workspace_id='w-002'",
      );
      expect(
        (await s.scan(f.actor, first.items[0]!.position, 1)).items[0]!.workspace.workspaceId,
      ).toBe("w-003");
      await admin.query(
        "UPDATE heimdall_managed.workspaces SET mutation_blocked=false WHERE product_id='scan-ts' AND workspace_id='w-002'",
      );
      await admin.query(
        "UPDATE heimdall_managed.membership_candidates SET observed_at=101 WHERE product_id='scan-ts' AND workspace_id='w-001'",
      );
      const updated = (await s.scan(f.actor, "0", 1)).items[0]!;
      expect(updated.position).toBe(first.items[0]!.position);
      expect(updated.revision).not.toBe(first.items[0]!.revision);
      // Immutable principal is corrupted at insert time in a separate isolated scope.
      await admin.query(
        "INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES('scan-bad-ts','production','bad','Bad','active',false,100,100)",
      );
      await admin.query(
        "INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at) VALUES('scan-bad-ts','production','bad',$1,'user:wrong',100)",
        [Buffer.from(f.identityHash, "hex")],
      );
      await expect(
        new CandidateRecords(p, "scan-bad-ts", "production").scan(f.actor, "0", 20),
      ).rejects.toThrow("unavailable");
      const readonly = new Pool({
        connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
        options: "-c default_transaction_read_only=on",
      });
      try {
        await expect(
          new CandidateRecords(readonly, "missing", "production").scan(f.actor, "0", 20),
        ).rejects.toThrow("unavailable");
      } finally {
        await readonly.end();
      }
      const blocker = await admin.connect();
      try {
        await blocker.query("BEGIN");
        await blocker.query(
          "LOCK TABLE heimdall_managed.membership_candidates IN ACCESS EXCLUSIVE MODE",
        );
        await expect(s.scan(f.actor, "0", 20, AbortSignal.timeout(100))).rejects.toThrow(
          "deadline_exceeded",
        );
      } finally {
        await blocker.query("ROLLBACK");
        blocker.release();
      }
      expect((await s.scan(f.actor, "0", 1)).items).toHaveLength(1);
    } finally {
      await p.end();
      await admin.end();
    }
  },
);
test.skipIf(!enabled || !peer)("candidate pages read Go completion and seeded order", async () => {
  const p = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  try {
    const s = new CandidateRecords(p, "scan-go", "production");
    const first = await s.scan(f.actor, "0", 100);
    expect(first.items).toHaveLength(100);
    expect((await s.scan(f.actor, first.nextPosition!, 100)).items[0]!.workspace.workspaceId).toBe(
      "w-101",
    );
    const journal = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/operation-journal.json", import.meta.url),
        "utf8",
      ),
    );
    expect(
      (
        await new CandidateRecords(p, "transition-go", "production").scan(
          journal.createIntent.actor,
          "0",
          100,
        )
      ).items.map((i) => i.workspace.workspaceId),
    ).toEqual(["complete"]);
  } finally {
    await p.end();
  }
});
