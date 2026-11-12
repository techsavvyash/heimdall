import { operationRecords, workspaceRecords } from "./managed-store.js";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
const read = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const f = read("operation-journal");
const vectors = read("membership-intents") as {
  kinds: { kind: string; roles: string[]; eventType: string }[];
  gates: { name: string; lifecycle: string; blocked: boolean }[];
  invalidRevisions: string[];
};
const intent = (scope: string, id: string, kind = "add_member", roles: string[] = ["viewer"]) =>
  JSON.stringify({
    ...f.createIntent,
    productId: scope,
    workspaceId: id,
    name: "",
    kind,
    roles,
    target: { ...f.createIntent.actor, subject: "target" },
  });
const allocation = (workspaceId: string, id: string, now = 200) => ({
  ...f.allocation,
  workspaceId,
  operationId: id,
  eventId: id,
  occurredAt: now,
  recordedAt: now,
});
const meta = (id: string, now: number) => ({
  ...allocation("unused", id, now),
  nextAttemptAt: now,
});
const store = (p: Pool, scope: string) =>
  operationRecords(p, { ...f.binding, productId: scope }, ["primary"]);
async function seed(p: Pool, scope: string, id: string, lifecycle = "active", blocked = false) {
  return (
    await p.query(
      "INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES($1,'production',$2,'Existing',$3,$4,100,100) RETURNING revision::text",
      [scope, id, lifecycle, blocked],
    )
  ).rows[0].revision as string;
}
test("membership intent inputs refuse creation and invalid revisions before I/O", async () => {
  const p = new Pool(),
    s = store(p, "membership-input");
  try {
    for (const revision of vectors.invalidRevisions)
      await expect(
        s.beginMembership(intent("membership-input", "w"), "key", allocation("w", "op"), revision),
      ).rejects.toThrow("invalid_argument");
    await expect(
      s.beginMembership(
        JSON.stringify({ ...f.createIntent, productId: "membership-input" }),
        "key",
        allocation("w", "op"),
        "1",
      ),
    ).rejects.toThrow("invalid_argument");
    await expect(
      s.create(intent("membership-input", "w"), "key", allocation("w", "op")),
    ).rejects.toThrow("invalid_argument");
    await expect(
      s.beginMembership(intent("membership-input", "w"), "key", allocation("other", "op"), "1"),
    ).rejects.toThrow("invalid_argument");
    await expect(
      s.beginMembership(
        intent("membership-input", "w"),
        "key",
        allocation("w", "op"),
        "1",
        AbortSignal.abort(),
      ),
    ).rejects.toThrow("cancelled");
  } finally {
    await p.end();
  }
});
const enabled = !!process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
  peer = !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER;
test.skipIf(!enabled || peer)(
  "membership intents reserve deduplicate and serialize with durable audit",
  async () => {
    const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
    const p = new Pool({
      connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
      options: "-c role=heimdall_managed_runtime",
    });
    const scope = "membership-ts",
      s = store(p, scope),
      w = workspaceRecords(p, scope, "production");
    try {
      for (const c of vectors.kinds) {
        const revision = await seed(admin, scope, c.kind);
        const raw = intent(scope, c.kind, c.kind, c.roles);
        const first = await s.beginMembership(
          raw,
          "same-key-per-kind",
          allocation(c.kind, c.kind),
          revision,
        );
        expect(first).toMatchObject({
          state: "pending",
          generation: 1,
          dispatchCount: 0,
          intent: { kind: c.kind, roles: c.roles },
        });
        const workspace = await w.get(c.kind);
        expect(workspace).toMatchObject({ lifecycle: "active", mutationBlocked: true });
        expect(workspace?.revision).not.toBe(revision);
        expect(
          await s.beginMembership(
            raw,
            "same-key-per-kind",
            allocation(c.kind, `duplicate-${c.kind}`),
            revision,
          ),
        ).toEqual(first);
        const wrapper = (
          await admin.query(
            "SELECT event FROM heimdall.audit_events WHERE product_id=$1 AND event_id=$2",
            [scope, c.kind],
          )
        ).rows[0].event;
        expect(JSON.parse(Buffer.from(wrapper.payload, "base64url").toString()).eventType).toBe(
          c.eventType,
        );
        await expect(
          s.beginMembership(
            JSON.stringify({
              ...JSON.parse(raw),
              target: { ...f.createIntent.actor, subject: "different" },
            }),
            "same-key-per-kind",
            allocation(c.kind, `changed-${c.kind}`),
            revision,
          ),
        ).rejects.toThrow("conflict");
        await s.advance(
          c.kind,
          f.createIntent.actor,
          1,
          "dispatch",
          meta(`dispatch-${c.kind}`, 201),
        );
        await s.advance(c.kind, f.createIntent.actor, 2, "confirm", meta(`confirm-${c.kind}`, 202));
        const done = await s.advance(
          c.kind,
          f.createIntent.actor,
          3,
          "complete",
          meta(`complete-${c.kind}`, 203),
        );
        expect((await w.get(c.kind))?.mutationBlocked).toBe(false);
        expect(
          await s.beginMembership(
            raw,
            "same-key-per-kind",
            allocation(c.kind, `terminal-${c.kind}`),
            revision,
          ),
        ).toEqual(done);
      }
      for (const c of vectors.gates) {
        const revision = await seed(admin, scope, c.name, c.lifecycle, c.blocked);
        await expect(
          s.beginMembership(
            intent(scope, c.name),
            c.name,
            allocation(c.name, `gate-${c.name}`),
            revision,
          ),
        ).rejects.toThrow("conflict");
      }
      const stale = await seed(admin, scope, "stale");
      await admin.query(
        "UPDATE heimdall_managed.workspaces SET updated_at=101 WHERE product_id=$1 AND workspace_id='stale'",
        [scope],
      );
      await expect(
        s.beginMembership(intent(scope, "stale"), "stale", allocation("stale", "stale"), stale),
      ).rejects.toThrow("conflict");
      const revision = await seed(admin, scope, "race");
      const results = await Promise.all(
        Array.from({ length: 8 }, (_, i) =>
          s.beginMembership(
            intent(scope, "race"),
            "race-key",
            allocation("race", `race-${i}`),
            revision,
          ),
        ),
      );
      expect(new Set(results.map((r) => r.operationId)).size).toBe(1);
      expect(
        (
          await admin.query(
            "SELECT count(*)::int AS n FROM heimdall_managed.operations WHERE product_id=$1 AND workspace_id='race'",
            [scope],
          )
        ).rows[0].n,
      ).toBe(1);
      expect(
        (
          await admin.query(
            "SELECT count(*)::int AS n FROM heimdall.audit_events WHERE product_id=$1 AND event_id LIKE 'race-%'",
            [scope],
          )
        ).rows[0].n,
      ).toBe(1);
      const uncertain = results[0]!;
      await s.advance(
        uncertain.operationId,
        f.createIntent.actor,
        1,
        "dispatch",
        meta("race-dispatch", 201),
      );
      await s.advance(
        uncertain.operationId,
        f.createIntent.actor,
        2,
        "uncertain",
        meta("race-uncertain", 202),
      );
      const blocked = await w.get("race");
      await expect(
        s.beginMembership(
          intent(scope, "race"),
          "successor",
          allocation("race", "successor", 203),
          blocked!.revision,
        ),
      ).rejects.toThrow("conflict");
      expect(
        (
          await s.beginMembership(
            intent(scope, "race"),
            "race-key",
            allocation("race", "uncertain-retry", 203),
            revision,
          )
        ).state,
      ).toBe("uncertain");
      const raceRevision = await seed(admin, scope, "competing");
      const competitors = await Promise.allSettled(
        Array.from({ length: 2 }, (_, i) =>
          s.beginMembership(
            intent(scope, "competing"),
            `different-${i}`,
            allocation("competing", `competing-${i}`),
            raceRevision,
          ),
        ),
      );
      expect(competitors.filter((r) => r.status === "fulfilled")).toHaveLength(1);
      expect(
        competitors
          .filter((r) => r.status === "rejected")
          .every((r) => r.reason.code === "conflict"),
      ).toBe(true);
      const rollbackRevision = await seed(admin, scope, "rollback"),
        before = await w.get("rollback");
      await expect(
        s.beginMembership(
          intent(scope, "rollback"),
          "rollback",
          { ...allocation("rollback", "rollback"), eventId: "add_member" },
          rollbackRevision,
        ),
      ).rejects.toThrow("conflict");
      expect(await w.get("rollback")).toEqual(before);
      expect(await s.get("rollback", f.createIntent.actor)).toBeNull();
      const cancelRevision = await seed(admin, scope, "cancel");
      await s.beginMembership(
        intent(scope, "cancel"),
        "cancel",
        allocation("cancel", "cancel"),
        cancelRevision,
      );
      await s.advance("cancel", f.createIntent.actor, 1, "cancel", meta("cancelled", 201));
      expect(await w.get("cancel")).toMatchObject({ lifecycle: "active", mutationBlocked: false });
      const nextRevision = (await w.get("cancel"))!.revision;
      expect(
        (
          await s.beginMembership(
            intent(scope, "cancel"),
            "after-cancel",
            allocation("cancel", "after-cancel", 202),
            nextRevision,
          )
        ).state,
      ).toBe("pending");
    } finally {
      await p.end();
      await admin.end();
    }
  },
);
test.skipIf(!enabled || !peer)("membership intents read and retry Go records", async () => {
  const p = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  try {
    const s = store(p, "membership-go");
    for (const c of vectors.kinds) {
      const original = await s.get(c.kind, f.createIntent.actor);
      expect(original?.state).toBe("completed");
      expect(
        await s.beginMembership(
          intent("membership-go", c.kind, c.kind, c.roles),
          "same-key-per-kind",
          allocation(c.kind, `peer-${c.kind}`, 300),
          "1",
        ),
      ).toEqual(original);
    }
  } finally {
    await p.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_MEMBERSHIP_LOSS)(
  "membership intent committed response loss retains reservation",
  async () => {
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL }),
      proxy = new Pool({
        connectionString: process.env.HEIMDALL_TEST_MEMBERSHIP_LOSS,
        options: "-c role=heimdall_managed_runtime",
      });
    try {
      const scope = "membership-loss-ts",
        revision = await seed(direct, scope, "lost"),
        raw = intent(scope, "lost");
      await expect(
        store(proxy, scope).beginMembership(raw, "key", allocation("lost", "lost"), revision),
      ).rejects.toMatchObject({ code: "outcome_unknown", operationId: "lost" });
      const s = store(direct, scope),
        r = await s.get("lost", f.createIntent.actor);
      expect(r).toMatchObject({ state: "pending", dispatchCount: 0 });
      expect(await s.beginMembership(raw, "key", allocation("lost", "retry"), revision)).toEqual(r);
      expect(
        (await workspaceRecords(direct, scope, "production").get("lost"))?.mutationBlocked,
      ).toBe(true);
    } finally {
      await proxy.end();
      await direct.end();
    }
  },
);
