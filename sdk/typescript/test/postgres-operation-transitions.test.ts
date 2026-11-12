import { operationRecords, workspaceRecords } from "./managed-store.js";
import type { OperationRecords } from "../src/internal/postgres/operations.js";
import { createHash } from "node:crypto";
import { encodeIdentity } from "../src/internal/identity-encoding.js";
import { intentDigests, type Intent } from "../src/internal/operations.js";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
const read = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const f = read("operation-journal");
const vectors = read("operation-transitions") as {
  scenarios: { name: string; steps: { event: string; expected: string }[] }[];
};
const meta = (id: string, now = 102) => ({
  eventId: id,
  requestId: "request",
  policyVersion: "policy",
  occurredAt: now,
  recordedAt: now,
  nextAttemptAt: now,
});
const allocation = (id: string) => ({
  ...f.allocation,
  workspaceId: id,
  operationId: id,
  eventId: `create-${id}`,
});
const store = (pool: Pool, scope: string) =>
  operationRecords(pool, { ...f.binding, productId: scope }, ["primary"]);
const create = (s: OperationRecords, scope: string, id: string) =>
  s.create(JSON.stringify({ ...f.createIntent, productId: scope }), id, allocation(id));
test("transition invalid inputs and pre-cancellation do not access storage", async () => {
  const p = new Pool();
  const s = store(p, "transition-input");
  try {
    await expect(s.advance("op", f.createIntent.actor, 0, "dispatch", meta("e"))).rejects.toThrow(
      "invalid_argument",
    );
    await expect(s.advance("op", f.createIntent.actor, 1, "force", meta("e"))).rejects.toThrow(
      "invalid_argument",
    );
    await expect(
      s.advance("op", f.createIntent.actor, 1, "dispatch", meta("e"), AbortSignal.abort()),
    ).rejects.toThrow("cancelled");
  } finally {
    await p.end();
  }
});
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("durable transitions preserve generations gates and candidates", async () => {
  const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  const p = new Pool({
    connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
    options: "-c role=heimdall_managed_runtime",
  });
  const scope = "transition-ts",
    s = store(p, scope),
    w = workspaceRecords(p, scope, f.binding.environment);
  try {
    for (const scenario of vectors.scenarios) {
      let record = await create(s, scope, scenario.name);
      let now = 102;
      for (const [index, step] of scenario.steps.entries()) {
        const call = s.advance(
          record.operationId,
          f.createIntent.actor,
          record.generation,
          step.event,
          meta(`${scenario.name}-${index}`, now++),
        );
        const outcome = await call.then(
          (r) => ({ record: r, result: r.state }),
          (e) => ({ record, result: e.code }),
        );
        expect(outcome.result).toBe(step.expected);
        expect(outcome.record.generation).toBe(
          record.generation + (step.expected === "conflict" ? 0 : 1),
        );
        expect(outcome.record.dispatchCount).toBe(
          record.dispatchCount +
            (step.expected !== "conflict" && step.event === "dispatch" ? 1 : 0),
        );
        record = outcome.record;
        expect(await s.get(record.operationId, f.createIntent.actor)).toEqual(record);
        const workspace = await w.get(record.workspaceId);
        expect(workspace?.lifecycle).toBe(
          record.state === "completed"
            ? "active"
            : record.state === "cancelled"
              ? "tombstoned"
              : "pending",
        );
        expect(workspace?.mutationBlocked).toBe(!["completed", "cancelled"].includes(record.state));
      }
    }
    expect(
      (
        await admin.query(
          "SELECT count(*)::int AS n FROM heimdall_managed.membership_candidates WHERE product_id=$1",
          [scope],
        )
      ).rows[0].n,
    ).toBe(1);
    const race = await create(s, scope, "race");
    const contenders = await Promise.allSettled(
      Array.from({ length: 8 }, (_, i) =>
        s.advance(race.operationId, f.createIntent.actor, 1, "dispatch", meta(`race-${i}`)),
      ),
    );
    expect(contenders.filter((r) => r.status === "fulfilled")).toHaveLength(1);
    expect(
      contenders.filter((r) => r.status === "rejected").every((r) => r.reason.code === "conflict"),
    ).toBe(true);
    expect((await s.get("race", f.createIntent.actor))?.dispatchCount).toBe(1);
    expect(
      (
        await admin.query(
          "SELECT count(*)::int AS n FROM heimdall.audit_events WHERE product_id=$1 AND event_id LIKE 'race-%'",
          [scope],
        )
      ).rows[0].n,
    ).toBe(1);
    await expect(
      s.advance("race", { ...f.createIntent.actor, subject: "wrong" }, 2, "confirm", meta("wrong")),
    ).rejects.toThrow("conflict");
    await expect(
      s.advance("race", f.createIntent.actor, 1, "confirm", meta("stale")),
    ).rejects.toThrow("conflict");
    const fail = await create(s, scope, "rollback");
    await expect(
      s.advance(fail.operationId, f.createIntent.actor, 1, "dispatch", meta("create-rollback")),
    ).rejects.toThrow("conflict");
    expect(await s.get(fail.operationId, f.createIntent.actor)).toEqual(fail);
    await s.advance("rollback", f.createIntent.actor, 1, "dispatch", meta("rollback-dispatch"));
    const confirmed = await s.advance(
      "rollback",
      f.createIntent.actor,
      2,
      "confirm",
      meta("rollback-confirm", 103),
    );
    await expect(
      s.advance("rollback", f.createIntent.actor, 3, "complete", meta("create-rollback", 104)),
    ).rejects.toThrow("conflict");
    expect(await s.get("rollback", f.createIntent.actor)).toEqual(confirmed);
    expect((await w.get("rollback"))?.mutationBlocked).toBe(true);
    expect(
      (
        await admin.query(
          "SELECT count(*)::int AS n FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND workspace_id='rollback'",
          [scope],
        )
      ).rows[0].n,
    ).toBe(0);
    const due = await create(s, scope, "due");
    await s.advance(due.operationId, f.createIntent.actor, 1, "pre_dispatch_failure", {
      ...meta("due-failure"),
      nextAttemptAt: 200,
    });
    await expect(
      s.advance("due", f.createIntent.actor, 2, "retry", meta("early", 199)),
    ).rejects.toThrow("conflict");
    await s.advance("due", f.createIntent.actor, 2, "retry", meta("due-retry", 200));
    await admin.query(
      "UPDATE heimdall_managed.operations SET generation=9007199254740991 WHERE product_id=$1 AND operation_id='due'",
      [scope],
    );
    await expect(
      s.advance(
        "due",
        f.createIntent.actor,
        Number.MAX_SAFE_INTEGER,
        "dispatch",
        meta("overflow", 201),
      ),
    ).rejects.toThrow("unavailable");
  } finally {
    await p.end();
    await admin.end();
  }
});
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("reads completed Go transition", async () => {
  const p = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  try {
    const r = await store(p, "transition-go").get("complete", f.createIntent.actor);
    expect(r).toMatchObject({ state: "completed", generation: 4, dispatchCount: 1 });
  } finally {
    await p.end();
  }
});
test.skipIf(!process.env.HEIMDALL_TEST_TRANSITION_LOSS)(
  "transition committed response loss retains dispatch quarantine",
  async () => {
    const direct = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
    const proxy = new Pool({
      connectionString: process.env.HEIMDALL_TEST_TRANSITION_LOSS,
      options: "-c role=heimdall_managed_runtime",
    });
    try {
      const s = store(direct, "transition-loss-ts");
      await create(s, "transition-loss-ts", "lost");
      await expect(
        store(proxy, "transition-loss-ts").advance(
          "lost",
          f.createIntent.actor,
          1,
          "dispatch",
          meta("lost-dispatch"),
        ),
      ).rejects.toMatchObject({ code: "outcome_unknown", operationId: "lost" });
      expect(await s.get("lost", f.createIntent.actor)).toMatchObject({
        state: "dispatching",
        generation: 2,
        dispatchCount: 1,
      });
      await expect(
        s.advance("lost", f.createIntent.actor, 1, "dispatch", meta("retry")),
      ).rejects.toThrow("conflict");
      expect(
        (await workspaceRecords(direct, "transition-loss-ts", f.binding.environment).get("lost"))
          ?.mutationBlocked,
      ).toBe(true);
    } finally {
      await proxy.end();
      await direct.end();
    }
  },
);

type MembershipCase = {
  name: string;
  kind: string;
  lifecycle: string;
  existing: boolean;
  observedAt: number;
  mismatch: boolean;
  error: boolean;
};
test.skipIf(
  !process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL || !!process.env.HEIMDALL_TEST_MANAGED_READ_PEER,
)("membership completion coordinates scoped candidates and lifecycle", async () => {
  const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL });
  const p = new Pool({
    connectionString: process.env.HEIMDALL_TEST_MANAGED_DATABASE_URL,
    options: "-c role=heimdall_managed_runtime",
  });
  const scope = "candidate-ts",
    s = store(p, scope),
    w = workspaceRecords(p, scope, f.binding.environment);
  const actor = f.createIntent.actor,
    target = { ...actor, subject: "new-owner" };
  const principal = (a: typeof actor) => encodeIdentity(a.issuer, a.subject);
  const hash = (a: typeof actor) => createHash("sha256").update(principal(a), "ascii").digest();
  try {
    for (const c of read("operation-transitions").membership as MembershipCase[]) {
      const i: Intent = {
        ...f.createIntent,
        productId: scope,
        kind: c.kind,
        workspaceId: c.name,
        name: "",
        roles: [],
        target,
      };
      const d = intentDigests(i, c.name);
      await admin.query(
        "INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES($1,$2,$3,'Membership',$4,true,101,103)",
        [scope, f.binding.environment, c.name, c.lifecycle],
      );
      await admin.query(
        `INSERT INTO heimdall_managed.operations(product_id,environment,operation_id,workspace_id,kind,actor_hash,target_hash,key_digest,fingerprint,intent_payload,store_id,model_id,model_hash,manifest_version,state,dispatch_count,generation,next_attempt_at,created_at,updated_at)
    VALUES($1,$2,$3,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'confirmed',1,3,103,101,103)`,
        [
          scope,
          f.binding.environment,
          c.name,
          c.kind,
          hash(actor),
          hash(target),
          Buffer.from(d.keyDigest, "hex"),
          Buffer.from(d.fingerprint, "hex"),
          Buffer.from(JSON.stringify(i)),
          f.binding.storeId,
          f.binding.modelId,
          Buffer.from(f.binding.modelHash, "hex"),
          f.binding.manifestVersion,
        ],
      );
      await admin.query(
        "INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at) VALUES($1,$2,$3,$4,$5,101)",
        [scope, f.binding.environment, c.name, hash(actor), principal(actor)],
      );
      if (c.existing)
        await admin.query(
          "INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at) VALUES($1,$2,$3,$4,$5,$6)",
          [
            scope,
            f.binding.environment,
            c.name,
            hash(target),
            principal(c.mismatch ? actor : target),
            c.observedAt,
          ],
        );
      const candidates = () =>
        admin.query(
          "SELECT identity_hash,principal,position::text,revision::text,observed_at::text FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND workspace_id=$2 ORDER BY position",
          [scope, c.name],
        );
      const before = (await candidates()).rows;
      const outcome = await s
        .advance(c.name, actor, 3, "complete", meta(`complete-${c.name}`, 200))
        .then(
          (r) => r.state,
          (e) => e.code,
        );
      expect(outcome).toBe(c.error ? "conflict" : "completed");
      expect((await w.get(c.name))?.lifecycle).toBe(c.lifecycle);
      expect((await w.get(c.name))?.mutationBlocked).toBe(c.error);
      const after = (await candidates()).rows;
      expect(after[0]).toEqual(before[0]);
      expect(after.length).toBe(c.error ? before.length : c.kind === "remove_member" ? 1 : 2);
      const retained = c.error ? after : after.slice(0, 1);
      expect(retained).toEqual(c.error ? before : before.slice(0, 1));
      const existingTarget = before.find((r) => r.identity_hash.equals(hash(target)));
      const resultingTarget = after.find((r) => r.identity_hash.equals(hash(target)));
      expect(resultingTarget?.position).toBe(
        c.kind === "remove_member"
          ? undefined
          : (existingTarget?.position ?? resultingTarget?.position),
      );
      expect((await s.get(c.name, actor))?.generation).toBe(c.error ? 3 : 4);
    }
  } finally {
    await p.end();
    await admin.end();
  }
});
