import { createHash } from "node:crypto";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { encodeIdentity } from "../src/internal/identity-encoding.js";
import {
  createDirectoryCleanup,
  createDirectoryPolling,
  createPostgresStorage,
} from "../src/postgres.js";

test.skipIf(!process.env.HEIMDALL_TEST_LIVE_SCIM)(
  "reconciles a live SCIM identity into PostgreSQL",
  async () => {
    const { subject, stage, ...reader } = JSON.parse(process.env.HEIMDALL_TEST_LIVE_SCIM!);
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_DIRECTORY_DB });
    try {
      const signal = AbortSignal.timeout(12000);
      const options = { ...reader, productId: "ledger", environment: "scim-live-ts" };
      const identity = { issuer: reader.issuer, subject };
      const hash = createHash("sha256").update(encodeIdentity(identity.issuer, subject)).digest();
      const worker = createDirectoryPolling(pool, options);
      const storage = createPostgresStorage(pool, { ...options, auditSinks: [] });
      const cleanup = createDirectoryCleanup(pool, options);
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
);
