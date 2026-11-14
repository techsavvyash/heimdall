import { createHash } from "node:crypto";
import { Pool } from "pg";
import assert from "node:assert/strict";
import { expect, test } from "vitest";
import { createHeimdall } from "../src/index.js";
import { encodeIdentity } from "../src/internal/identity-encoding.js";
import {
  createDirectoryCleanup,
  createDirectoryDeactivation,
  createDirectoryEnrollment,
  createDirectoryPolling,
  createDirectoryReactivation,
  createDirectoryReactivationRelease,
  createManagedPostgresStorage,
  createPostgresStorage,
} from "../src/postgres.js";

test.skipIf(!process.env.HEIMDALL_TEST_LIVE_SCIM)(
  "reconciles a live SCIM identity into PostgreSQL",
  async () => {
    const fixture = JSON.parse(process.env.HEIMDALL_TEST_LIVE_SCIM!) as LiveSCIMFixture;
    const { subject, stage, issuer, organizationId, token } = fixture;
    const reader = { issuer, organizationId, token };
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DIRECTORY_DB });
    try {
      const signal = AbortSignal.timeout(12000);
      const scope = { ...reader, productId: "ledger", environment: "verification", auditSinks: [] };
      if (stage === "stale-reactivation") {
        await rejectStaleReactivation(pool, scope, fixture, signal);
        return;
      }
      if (stage.startsWith("cleanup") || stage === "reenroll") {
        await verifyLiveSCIMReactivation(pool, fixture, signal);
        return;
      }
      const options = { ...reader, productId: "ledger", environment: "scim-live-ts" };
      const identity = { issuer: reader.issuer, subject };
      const hash = createHash("sha256").update(encodeIdentity(identity.issuer, subject)).digest();
      const worker = createDirectoryPolling(pool, options);
      const storage = createPostgresStorage(pool, { ...options, auditSinks: [] });
      const cleanup = createDirectoryCleanup(pool, options);
      if (stage === "browser") {
        const observed = await createDirectoryDeactivation(pool, scope)(subject, signal);
        if (
          observed.state !== "inactive" ||
          observed.sessionsInvalidated !== 1 ||
          !(await createPostgresStorage(pool, scope).identity.check(identity, signal))
        )
          throw new Error("Live SCIM disable did not revoke the browser session");
        return;
      }
      const scopes = [options.environment, "scim-live-other-ts"];
      if (stage === "seed") {
        for (const environment of scopes) {
          await pool.query(
            "INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at,identity_hash) VALUES($1,$2,'session',$3,'fixture','fixture',0,$3)",
            [options.productId, environment, hash],
          );
        }
        const discovered = await worker.discover(50, signal);
        if (discovered.discovered !== 1 || !discovered.cycleComplete)
          throw new Error("Unexpected live directory discovery");
      }
      const observed = await worker.observeKnown(null, 2, signal);
      expect(observed).toMatchObject({
        observed: 1,
        sessionsInvalidated: stage === "inactive" ? 1 : 0,
      });
      expect(await storage.identity.check(identity, signal)).toBe(stage !== "seed");
      const pending = await cleanup.pending(null, 2, signal);
      expect(pending.map(({ reason }) => reason)).toEqual(
        stage === "seed" ? [] : [stage === "missing" ? "deleted" : "inactive"],
      );
      const { rows } = await pool.query(
        "SELECT environment FROM heimdall.records WHERE product_id=$1 AND identity_hash=$2 AND environment=ANY($3) ORDER BY environment",
        [options.productId, hash, scopes],
      );
      expect(rows.map(({ environment }) => environment)).toEqual(
        stage === "seed" ? [...scopes].sort() : ["scim-live-other-ts"],
      );
    } finally {
      await pool.end();
    }
  },
  15000,
);

type LiveSCIMFixture = {
  subject: string;
  stage: string;
  issuer: string;
  organizationId: string;
  token: string;
  config: unknown;
  manifest: unknown;
  operatorToken: string;
  clientSecret: string;
  openfgaKey: string;
  workspaceId: string;
  approvedRole: string;
  revision: string;
};

async function rejectStaleReactivation(
  pool: Pool,
  scope: Parameters<typeof createDirectoryDeactivation>[1] & { auditSinks: string[] },
  { subject, revision, workspaceId }: LiveSCIMFixture,
  signal: AbortSignal,
) {
  const prepare = createDirectoryReactivation(pool, scope);
  const release = createDirectoryReactivationRelease(pool, scope);
  const input = { subject, revision, enrollments: [{ workspaceId, ruleId: "browser-scim-v1" }] };
  console.info("SCIM_STEP stale-provider-state");
  await assert.rejects(prepare(subject, revision, signal), { code: "forbidden" });
  await assert.rejects(release(input, signal), { code: "forbidden" });
  const observe = createDirectoryDeactivation(pool, scope);
  assert.deepEqual(await observe(subject, signal), { state: "inactive", sessionsInvalidated: 1 });
  await assert.rejects(release(input, signal), { code: "forbidden" });
  assert.equal(
    await createPostgresStorage(pool, scope).identity.check(
      { issuer: scope.issuer, subject },
      signal,
    ),
    true,
  );
}

async function verifyLiveSCIMReactivation(
  pool: Pool,
  fixture: LiveSCIMFixture,
  signal: AbortSignal,
) {
  const {
    stage,
    subject,
    issuer,
    organizationId,
    token,
    config,
    manifest,
    operatorToken,
    clientSecret,
    openfgaKey,
    workspaceId,
    approvedRole,
    revision,
  } = fixture;
  const scope = {
    issuer,
    organizationId,
    token,
    productId: "ledger",
    environment: "verification",
    auditSinks: [],
  };
  console.info("SCIM_STEP runtime");
  const storage = createManagedPostgresStorage(pool, scope);
  const runtime = await createHeimdall(
    JSON.stringify(config),
    JSON.stringify(manifest),
    {
      policyVersion: "reference-v1",
      credentials: {
        clientSecret: async () => clientSecret,
        openfgaKey: async () => openfgaKey,
      },
      storage,
    },
    { signal },
  );
  console.info("SCIM_STEP operator");
  const actor = await runtime.tokens.verify(operatorToken, { signal });
  const identity = { issuer, subject };
  assert.equal(await storage.identity.check(identity, signal), true);
  if (stage.startsWith("cleanup")) {
    console.info("SCIM_STEP cleanup");
    const cleanup = createDirectoryCleanup(pool, scope);
    const pending = await cleanup.pending(null, 2, signal);
    if (stage === "cleanup-replay") {
      assert.deepEqual(pending, []);
    } else {
      assert.equal(pending.length, 1);
      assert.equal(pending[0]?.subject, subject);
      assert.equal(pending[0]?.reason, "inactive");
    }
    const cleanupRevision = pending[0]?.revision ?? revision;
    const result = await cleanup.reconcile(
      runtime,
      actor,
      {
        subject,
        revision: cleanupRevision,
        workspaces: [workspaceId],
      },
      signal,
    );
    assert.deepEqual(result, { status: "confirmed" });
    assert.deepEqual(await cleanup.pending(null, 2, signal), []);
    process.stdout.write(`SCIM_REVISION ${cleanupRevision}\n`);
    return;
  }
  const ruleId = "browser-scim-v1";
  const approval = { subject, revision, enrollments: [{ workspaceId, ruleId }] };
  console.info("SCIM_STEP prepare");
  await createDirectoryReactivation(pool, scope)(subject, revision, signal);
  const release = createDirectoryReactivationRelease(pool, scope);
  await assert.rejects(release(approval, signal), { code: "conflict" });
  console.info("SCIM_STEP enroll");
  const result = await createDirectoryEnrollment(pool, scope)(
    runtime,
    actor,
    {
      subject,
      workspaceId,
      ruleId,
      roles: [approvedRole],
      reactivationRevision: revision,
    },
    signal,
  );
  assert.equal(result.status, "confirmed");
  assert.equal(await storage.identity.check(identity, signal), true);
  console.info("SCIM_STEP release");
  await release(approval, signal);
  assert.equal(await storage.identity.check(identity, signal), false);
  return;
}
