import { Pool } from "pg";
import { expect, test, vi } from "vitest";
import {
  createDirectoryCleanup,
  createDirectoryDeactivation,
  createDirectoryPolling,
  createDirectoryReactivation,
  createPostgresStorage,
} from "../src/postgres.js";
import { encodeIdentity } from "../src/internal/identity-encoding.js";
import { createHash } from "node:crypto";

test.skipIf(!process.env.HEIMDALL_TEST_DIRECTORY_DB)(
  "records a replayable directory intent and denies sessions atomically",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DIRECTORY_DB });
    const issuer = "https://zitadel.example";
    const productId = "ledger";
    const environment = "directory-ts";
    const identity = { issuer, subject: "employee" };
    const options = { issuer, productId, environment, organizationId: "org-a", token: "viewer" };
    const signal = AbortSignal.timeout(15000);
    let owner = "org-a";
    let state = "USER_STATE_INACTIVE";
    let listUnavailable = false;
    vi.stubGlobal("fetch", async (url: string) => {
      const start = new URL(url).searchParams.get("startIndex");
      if (url.endsWith("/scim/v2/org-a/Users/employee"))
        return Response.json({ id: identity.subject, active: true, externalId: "directory-key" });
      if (start !== null && listUnavailable) return new Response(null, { status: 503 });
      if (start !== null)
        return Response.json({
          startIndex: Number(start),
          totalResults: 1,
          Resources: start === "1" ? [{ id: identity.subject, externalId: "directory-key" }] : [],
        });
      return Response.json({
        user: {
          userId: identity.subject,
          details: { resourceOwner: owner },
          state,
          human: {},
        },
      });
    });
    try {
      const port = createPostgresStorage(pool, { productId, environment, auditSinks: [] });
      const observe = createDirectoryDeactivation(pool, options);
      const hash = createHash("sha256").update(encodeIdentity(issuer, identity.subject)).digest();
      const insert = (scope: string) =>
        pool.query(
          "INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at,identity_hash) VALUES($1,$2,'session',$3,'fixture','test',0,$3)",
          [productId, scope, hash],
        );
      await insert(environment);
      await insert("directory-other-ts");
      const rollback = "directory-rollback-ts";
      await insert(rollback);
      await expect(
        createDirectoryDeactivation(pool, { ...options, environment: rollback })(
          identity.subject,
          signal,
        ),
      ).rejects.toMatchObject({ code: "unavailable" });
      await expect(
        createPostgresStorage(pool, {
          productId,
          environment: rollback,
          auditSinks: [],
        }).identity.check(identity, signal),
      ).resolves.toBe(false);
      expect(
        (
          await pool.query(
            "SELECT count(*)::int AS count FROM heimdall_directory.deactivations WHERE environment=$1",
            [rollback],
          )
        ).rows,
      ).toEqual([{ count: 0 }]);
      owner = "org-b";
      await expect(observe(identity.subject, signal)).rejects.toMatchObject({ code: "conflict" });
      await expect(port.identity.check(identity, signal)).resolves.toBe(false);
      owner = "org-a";
      await expect(observe(identity.subject, signal)).resolves.toEqual({
        state: "inactive",
        sessionsInvalidated: 1,
      });
      await expect(port.identity.check(identity, signal)).resolves.toBe(true);
      const pollOptions = { ...options, environment: "directory-poll-ts" };
      const polling = createDirectoryPolling(pool, pollOptions);
      await expect(polling.discover(1, signal)).resolves.toEqual({
        discovered: 1,
        cycleComplete: false,
      });
      const restarted = createDirectoryPolling(pool, pollOptions);
      listUnavailable = true;
      await expect(restarted.discover(1, signal)).rejects.toMatchObject({ code: "unavailable" });
      listUnavailable = false;
      await expect(restarted.discover(1, signal)).resolves.toEqual({
        discovered: 0,
        cycleComplete: true,
      });
      await expect(restarted.discover(1, signal)).resolves.toEqual({
        discovered: 1,
        cycleComplete: false,
      });
      await expect(restarted.observeKnown(null, 2, signal)).resolves.toEqual({
        observed: 1,
        sessionsInvalidated: 0,
        nextSubject: null,
      });
      await expect(
        createDirectoryCleanup(pool, pollOptions).pending(null, 1, signal),
      ).resolves.toMatchObject([{ subject: identity.subject }]);
      await expect(observe(identity.subject, signal)).resolves.toEqual({
        state: "inactive",
        sessionsInvalidated: 0,
      });
      state = "USER_STATE_INITIAL";
      await expect(observe(identity.subject, signal)).resolves.toEqual({
        state: "initial",
        sessionsInvalidated: 0,
      });
      await expect(port.identity.check(identity, signal)).resolves.toBe(true);
      state = "USER_STATE_INACTIVE";
      owner = "org-b";
      await expect(
        createDirectoryDeactivation(pool, { ...options, organizationId: owner })(
          identity.subject,
          signal,
        ),
      ).rejects.toMatchObject({ code: "conflict" });
      expect(
        (
          await pool.query(
            "SELECT organization_id,reason FROM heimdall_directory.deactivations WHERE product_id=$1 AND environment=$2",
            [productId, environment],
          )
        ).rows,
      ).toEqual([{ organization_id: "org-a", reason: "inactive" }]);
      expect(
        (
          await pool.query(
            "SELECT environment FROM heimdall.records WHERE product_id=$1 AND identity_hash=$2 AND environment IN ($3,$4)",
            [productId, hash, environment, "directory-other-ts"],
          )
        ).rows,
      ).toEqual([{ environment: "directory-other-ts" }]);
      const cleanup = createDirectoryCleanup(pool, options);
      const [pending] = await cleanup.pending(null, 1, signal);
      expect(pending).toMatchObject({ subject: identity.subject, reason: "inactive" });
      const prepare = createDirectoryReactivation(pool, options);
      state = "USER_STATE_ACTIVE";
      owner = "org-a";
      await expect(prepare(identity.subject, pending!.revision, signal)).rejects.toMatchObject({
        code: "conflict",
      });
      await expect(cleanup.pending(pending!.revision, 1, signal)).resolves.toEqual([]);
      await expect(
        createDirectoryCleanup(pool, { ...options, organizationId: "org-b" }).pending(
          null,
          1,
          signal,
        ),
      ).resolves.toEqual([]);
      await cleanup.complete(identity.subject, pending!.revision, signal);
      await cleanup.complete(identity.subject, pending!.revision, signal);
      await expect(cleanup.pending(null, 1, signal)).resolves.toEqual([]);
      owner = "org-a";
      state = "USER_STATE_INACTIVE";
      await observe(identity.subject, signal);
      await expect(cleanup.pending(null, 1, signal)).resolves.toEqual([]);
      state = "USER_STATE_ACTIVE";
      const prepared = await prepare(identity.subject, pending!.revision, signal);
      await expect(prepare(identity.subject, pending!.revision, signal)).resolves.toEqual(prepared);
      await expect(port.identity.check(identity, signal)).resolves.toBe(true);
      state = "USER_STATE_INACTIVE";
      await observe(identity.subject, signal);
      const [again] = await cleanup.pending(null, 1, signal);
      expect(again!.revision).not.toBe(pending!.revision);
      state = "USER_STATE_ACTIVE";
      await expect(prepare(identity.subject, pending!.revision, signal)).rejects.toMatchObject({
        code: "conflict",
      });
      state = "USER_STATE_DELETED";
      await observe(identity.subject, signal);
      await expect(
        cleanup.complete(identity.subject, pending!.revision, signal),
      ).rejects.toMatchObject({ code: "conflict" });
      const [deleted] = await cleanup.pending(null, 1, signal);
      expect(deleted).toMatchObject({ subject: identity.subject, reason: "deleted" });
      expect(deleted!.revision).not.toBe(pending!.revision);
      await cleanup.complete(identity.subject, deleted!.revision, signal);
      state = "USER_STATE_ACTIVE";
      await expect(prepare(identity.subject, deleted!.revision, signal)).rejects.toMatchObject({
        code: "conflict",
      });
      await expect(port.identity.check(identity, signal)).resolves.toBe(true);
    } finally {
      vi.unstubAllGlobals();
      await pool.end();
    }
  },
  20000,
);
