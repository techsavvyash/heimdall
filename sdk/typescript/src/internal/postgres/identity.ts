import { createHash } from "node:crypto";
import type { Pool } from "pg";
import { encodeIdentity } from "../identity-encoding.js";
import type { IdentityVerifier } from "../identity-verifier.js";
import { parseIdentityContext } from "../identity-context.js";
import { HeimdallError, checkCancellation } from "../errors.js";
import { retentionSQL, retentionArgs, lockRetentionAdmission } from "./readiness.js";
import { query, transaction } from "./query.js";

// Identity denial and exact-session invalidation never decrypt session payloads.
export class IdentityRecords {
  private retainedRuntime = false;
  static forRetainedRuntime(pool: Pool, productId: string, environment: string) {
    const records = new IdentityRecords(pool, productId, environment);
    records.retainedRuntime = true;
    return records.identityGate(productId, environment);
  }

  constructor(
    private readonly pool: Pool,
    private readonly productId: string,
    private readonly environment: string,
    private readonly now: () => number = Date.now,
  ) {
    for (const value of [productId, environment])
      if (typeof value !== "string" || !/^[A-Za-z0-9_-]{1,64}(?![\s\S])/.test(value))
        throw new HeimdallError("invalid_argument");
    if (!pool || typeof pool.connect !== "function" || typeof now !== "function")
      throw new HeimdallError("invalid_argument");
  }
  // Bind to the verifier's immutable scope before attaching this durable gate.
  identityGate(
    productId: string,
    environment: string,
  ): ConstructorParameters<typeof IdentityVerifier>[2] {
    if (productId !== this.productId || environment !== this.environment)
      throw new HeimdallError("invalid_argument");
    return {
      check: (identity, signal) => this.isIdentityDenied(identity.issuer, identity.subject, signal),
      invalidate: async (negative, signal) => {
        checkCancellation(signal);
        const context = parseIdentityContext(negative.context);
        if (!/^[0-9a-f]{64}$/.test(negative.tokenDigest))
          throw new HeimdallError("invalid_argument");
        if (negative.reason === "token_inactive") {
          if (context)
            await this.invalidateSessionVersion(
              context.key,
              context.revision,
              negative.issuer,
              negative.subject,
              signal,
            );
          return;
        }
        if (negative.reason !== "identity_inactive" && negative.reason !== "identity_deleted")
          throw new HeimdallError("invalid_argument");
        await this.invalidateIdentity(
          negative.issuer,
          negative.subject,
          this.now(),
          negative.reason === "identity_deleted" ? "deleted" : "inactive",
          signal,
        );
      },
    };
  }
  identity(issuer: string, subject: string) {
    return createHash("sha256").update(encodeIdentity(issuer, subject), "ascii").digest();
  }
  lock(identity: Buffer) {
    return createHash("sha256")
      .update(
        `heimdall.identity.v1|${this.productId}|${this.environment}|${identity.toString("hex")}`,
        "ascii",
      )
      .digest()
      .readBigInt64BE()
      .toString();
  }
  async invalidateSessionVersion(
    key: string,
    revision: string,
    issuer: string,
    subject: string,
    signal?: AbortSignal,
  ) {
    if (
      typeof revision !== "string" ||
      !/^[1-9][0-9]{0,18}(?![\s\S])/.test(revision) ||
      BigInt(revision) > 9223372036854775807n
    )
      throw new HeimdallError("invalid_argument");
    const hash = sessionKeyHash(key);
    const identity = this.identity(issuer, subject);
    return transaction(
      this.pool,
      async (client) => {
        if (this.retainedRuntime) await lockRetentionAdmission(client);
        await client.query("SELECT pg_advisory_xact_lock($1::bigint)", [this.lock(identity)]);
        const removed = await client.query(
          "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$3 AND identity_hash=$4 AND revision=$5",
          [this.productId, this.environment, hash, identity, revision],
        );
        return removed.rowCount === 1;
      },
      signal,
    );
  }
  async isIdentityDenied(issuer: string, subject: string, signal?: AbortSignal) {
    const identity = this.identity(issuer, subject);
    const rows = await query<{ denied: boolean; ready?: boolean }>(
      this.pool,
      this.retainedRuntime
        ? `SELECT readiness.ready, EXISTS(SELECT 1 FROM heimdall.identity_denials WHERE product_id=$7 AND environment=$8 AND identity_hash=$9) AS denied FROM (${retentionSQL}) readiness`
        : "SELECT EXISTS(SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$3) AS denied",
      [...(this.retainedRuntime ? retentionArgs : []), this.productId, this.environment, identity],
      false,
      signal,
    );
    if ((this.retainedRuntime && rows[0]?.ready !== true) || typeof rows[0]?.denied !== "boolean")
      throw new HeimdallError("unavailable");
    return rows[0].denied;
  }
  async invalidateIdentity(
    issuer: string,
    subject: string,
    observedAt: number,
    reason: "inactive" | "deleted",
    signal?: AbortSignal,
  ) {
    if (
      !Number.isSafeInteger(observedAt) ||
      observedAt < 0 ||
      !["inactive", "deleted"].includes(reason)
    )
      throw new HeimdallError("invalid_argument");
    const identity = this.identity(issuer, subject);
    return transaction(
      this.pool,
      async (client) => {
        if (this.retainedRuntime) await lockRetentionAdmission(client);
        await client.query("SELECT pg_advisory_xact_lock($1::bigint)", [this.lock(identity)]);
        await client.query(
          "INSERT INTO heimdall.identity_denials(product_id,environment,identity_hash,observed_at,reason) VALUES($1,$2,$3,$4,$5) ON CONFLICT(product_id,environment,identity_hash) DO UPDATE SET observed_at=GREATEST(identity_denials.observed_at,EXCLUDED.observed_at),reason=CASE WHEN identity_denials.reason='deleted' OR EXCLUDED.reason='deleted' THEN 'deleted' ELSE 'inactive' END",
          [this.productId, this.environment, identity, observedAt, reason],
        );
        const removed = await client.query(
          "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND identity_hash=$3",
          [this.productId, this.environment, identity],
        );
        return removed.rowCount!;
      },
      signal,
    );
  }
}

function sessionKeyHash(key: string) {
  if (typeof key !== "string" || !/^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(key))
    throw new HeimdallError("invalid_argument");
  return createHash("sha256").update(key, "ascii").digest();
}
