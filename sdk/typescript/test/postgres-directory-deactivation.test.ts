import { Pool } from "pg";
import { expect, test, vi } from "vitest";
import { createDirectoryDeactivation, createPostgresStorage } from "../src/postgres.js";
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
    vi.stubGlobal("fetch", async () =>
      Response.json({
        user: {
          userId: identity.subject,
          details: { resourceOwner: owner },
          state,
          human: {},
        },
      }),
    );
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
    } finally {
      vi.unstubAllGlobals();
      await pool.end();
    }
  },
  20000,
);
