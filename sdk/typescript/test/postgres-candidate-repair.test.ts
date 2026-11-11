import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { OperationRecords } from "../src/internal/postgres/operations.js";
import { CandidateRecords } from "../src/internal/postgres/candidates.js";
import { WorkspaceRecords } from "../src/internal/postgres/workspaces.js";
const read = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const f = read("operation-journal"),
  v = read("candidate-repair") as {
    observations: {
      identity: { issuer: string; subject: string };
      member: boolean;
      observedAt: number;
    }[];
    gates: { lifecycle: string; blocked: boolean }[];
  };
const store = (p: Pool, scope: string) =>
  new OperationRecords(p, { ...f.binding, productId: scope }, ["primary"]);
const metadata = (id: string, now = 200) => ({
  eventId: id,
  requestId: "request",
  policyVersion: "policy",
  occurredAt: now,
  recordedAt: now,
});
const seed = async (p: Pool, scope: string, id: string, lifecycle = "active", blocked = false) =>
  (
    await p.query(
      "INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES($1,'production',$2,'Repair',$3,$4,100,100) RETURNING revision::text",
      [scope, id, lifecycle, blocked],
    )
  ).rows[0].revision as string;
const enabled = !!process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
  peer = !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER;
test("candidate repair rejects invalid batches before I/O", async () => {
  const p = new Pool(),
    s = store(p, "repair-input");
  try {
    await expect(s.repairCandidates("w", "1", [], metadata("e"))).rejects.toThrow(
      "invalid_argument",
    );
    await expect(s.repairCandidates("w", "01", v.observations, metadata("e"))).rejects.toThrow(
      "invalid_argument",
    );
    await expect(
      s.repairCandidates("w", "1", [v.observations[0]!, v.observations[0]!], metadata("e")),
    ).rejects.toThrow("invalid_argument");
    await expect(
      s.repairCandidates(
        "w",
        "1",
        Array.from({ length: 101 }, () => v.observations[0]!),
        metadata("e"),
      ),
    ).rejects.toThrow("invalid_argument");
    await expect(
      s.repairCandidates("w", "1", [{ ...v.observations[0]!, observedAt: 201 }], metadata("e")),
    ).rejects.toThrow("invalid_argument");
    await expect(
      s.repairCandidates("w", "1", v.observations, metadata("e"), AbortSignal.abort()),
    ).rejects.toThrow("cancelled");
  } finally {
    await p.end();
  }
});
test.skipIf(!enabled || peer)(
  "candidate repair serializes with mutations and rolls back with audit",
  async () => {
    const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL }),
      p = new Pool({
        connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
        options: "-c role=heimdall_managed_runtime",
      });
    const scope = "repair-ts",
      s = store(p, scope),
      w = new WorkspaceRecords(p, scope, "production");
    const candidates = async (id: string) =>
      (
        await admin.query(
          "SELECT principal,position::text,revision::text,observed_at::text FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND workspace_id=$2 ORDER BY position",
          [scope, id],
        )
      ).rows;
    try {
      const revision = await seed(admin, scope, "w");
      const first = await s.repairCandidates("w", revision, v.observations, metadata("first"));
      expect(first).toMatchObject({ lifecycle: "active", mutationBlocked: false });
      expect(first.revision).not.toBe(revision);
      const both = await candidates("w");
      expect(both).toHaveLength(2);
      await expect(
        s.repairCandidates("w", revision, v.observations, metadata("stale")),
      ).rejects.toThrow("conflict");
      const removed = await s.repairCandidates(
        "w",
        first.revision,
        [{ ...v.observations[0]!, member: false, observedAt: 201 }],
        metadata("removed", 201),
      );
      expect(await candidates("w")).toEqual([both[1]]);
      const before = await w.get("w");
      await expect(
        s.repairCandidates(
          "w",
          removed.revision,
          [
            { ...v.observations[1]!, member: false, observedAt: 202 },
            { ...v.observations[0]!, observedAt: 202 },
          ],
          metadata("first", 202),
        ),
      ).rejects.toThrow("conflict");
      expect(await w.get("w")).toEqual(before);
      expect(await candidates("w")).toEqual([both[1]]);
      await expect(
        s.repairCandidates(
          "w",
          removed.revision,
          [{ ...v.observations[1]!, observedAt: 199 }],
          metadata("old-observation", 202),
        ),
      ).rejects.toThrow("conflict");
      expect(await w.get("w")).toEqual(before);
      const updated = await s.repairCandidates(
        "w",
        removed.revision,
        [{ ...v.observations[1]!, observedAt: 202 }],
        metadata("updated", 202),
      );
      const after = await candidates("w");
      expect(after[0].position).toBe(both[1].position);
      expect(after[0].revision).not.toBe(both[1].revision);
      expect(updated.revision).not.toBe(removed.revision);
      await expect(
        s.repairCandidates(
          "w",
          updated.revision,
          [v.observations[0]!],
          metadata("old-absent", 203),
        ),
      ).rejects.toThrow("conflict");
      for (const [n, g] of v.gates.entries()) {
        const rev = await seed(admin, scope, `gate-${n}`, g.lifecycle, g.blocked);
        await expect(
          s.repairCandidates(`gate-${n}`, rev, v.observations, metadata(`gate-${n}`)),
        ).rejects.toThrow("conflict");
      }
      const raceRevision = await seed(admin, scope, "race");
      const intent = JSON.stringify({
        ...f.createIntent,
        productId: scope,
        kind: "add_member",
        workspaceId: "race",
        name: "",
        roles: [],
        target: v.observations[0]!.identity,
      });
      const outcomes = await Promise.allSettled([
        s.repairCandidates("race", raceRevision, v.observations, metadata("race-repair")),
        s.beginMembership(
          intent,
          "race",
          {
            ...f.allocation,
            workspaceId: "race",
            operationId: "race-mutation",
            eventId: "race-mutation",
            occurredAt: 200,
            recordedAt: 200,
          },
          raceRevision,
        ),
      ]);
      expect(outcomes.filter((r) => r.status === "fulfilled")).toHaveLength(1);
      expect(
        outcomes.filter((r) => r.status === "rejected").every((r) => r.reason.code === "conflict"),
      ).toBe(true);
      expect(
        (
          await admin.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_events WHERE product_id=$1 AND event_id IN ('race-repair','race-mutation')",
            [scope],
          )
        ).rows[0].n,
      ).toBe(1);
      // A completed managed change invalidates observations made before it, not only while blocked.
      const gateRev = await seed(admin, scope, "changed");
      const raw = JSON.stringify({ ...JSON.parse(intent), workspaceId: "changed" });
      await s.beginMembership(
        raw,
        "changed",
        {
          ...f.allocation,
          workspaceId: "changed",
          operationId: "changed",
          eventId: "changed",
          occurredAt: 200,
          recordedAt: 200,
        },
        gateRev,
      );
      await s.advance("changed", f.createIntent.actor, 1, "cancel", {
        ...metadata("changed-cancel", 201),
        nextAttemptAt: 201,
      });
      await expect(
        s.repairCandidates("changed", gateRev, v.observations, metadata("late", 202)),
      ).rejects.toThrow("conflict");
      expect(await candidates("changed")).toEqual([]);
    } finally {
      await p.end();
      await admin.end();
    }
  },
);
test.skipIf(!enabled || !peer)(
  "candidate repair reads Go result and cannot replay old revision",
  async () => {
    const p = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
    try {
      const w = await new WorkspaceRecords(p, "repair-go", "production").get("w");
      expect(
        (
          await new CandidateRecords(p, "repair-go", "production").scan(
            v.observations[1]!.identity,
            "0",
            100,
          )
        ).items
          .filter((x) => x.workspace.workspaceId === "w")
          .map((x) => x.workspace.workspaceId),
      ).toEqual(["w"]);
      expect(w).toMatchObject({ lifecycle: "active", mutationBlocked: false });
      expect(
        (
          await p.query(
            "SELECT count(*)::int AS n FROM heimdall_managed.membership_candidates WHERE product_id='repair-go' AND workspace_id='w'",
          )
        ).rows[0].n,
      ).toBe(1);
      await expect(
        store(p, "repair-go").repairCandidates("w", "1", v.observations, metadata("peer", 300)),
      ).rejects.toThrow("conflict");
    } finally {
      await p.end();
    }
  },
);
test.skipIf(!process.env.HEIMDALL_TEST_REPAIR_LOSS)(
  "candidate repair committed response loss refuses stale replay",
  async () => {
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL }),
      proxy = new Pool({
        connectionString: process.env.HEIMDALL_TEST_REPAIR_LOSS,
        options: "-c role=heimdall_managed_runtime",
      });
    try {
      const scope = "repair-loss-ts",
        revision = await seed(direct, scope, "w");
      await expect(
        store(proxy, scope).repairCandidates("w", revision, v.observations, metadata("lost")),
      ).rejects.toMatchObject({ code: "outcome_unknown" });
      const w = await new WorkspaceRecords(direct, scope, "production").get("w");
      expect(w?.revision).not.toBe(revision);
      expect(
        (
          await direct.query(
            "SELECT count(*)::int AS n FROM heimdall_managed.membership_candidates WHERE product_id=$1",
            [scope],
          )
        ).rows[0].n,
      ).toBe(2);
      await expect(
        store(direct, scope).repairCandidates("w", revision, v.observations, metadata("retry")),
      ).rejects.toThrow("conflict");
    } finally {
      await proxy.end();
      await direct.end();
    }
  },
);
