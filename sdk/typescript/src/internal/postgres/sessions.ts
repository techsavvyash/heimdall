import { createHash } from "node:crypto";
import type { Pool } from "pg";
import type { RecordCipher } from "../envelopes.js";
import { encodeIdentity } from "../identity-encoding.js";
import { HeimdallError } from "../errors.js";
import { PostgresRecords, columns } from "./records.js";
import type { Row } from "./records.js";
import { query, transaction } from "./query.js";
type Principal = { productId: string; environment: string; issuer: string; subject: string };
// Validation includes the session schema and returns its exact principal/scope.
// Provider authentication and freshness remain the calling SDK layer's job.
export class SessionRecords extends PostgresRecords {
  constructor(
    pool: Pool,
    cipher: RecordCipher,
    productId: string,
    environment: string,
    private readonly validate: (value: Uint8Array) => Principal,
    now: () => number = Date.now,
  ) {
    super(pool, cipher, productId, environment, now);
    if (typeof validate !== "function") throw new HeimdallError("invalid_argument");
  }
  private identity(issuer: string, subject: string) {
    return createHash("sha256").update(encodeIdentity(issuer, subject), "ascii").digest();
  }
  private principal(value: Uint8Array) {
    const principal = this.validate(value);
    if (principal.productId !== this.productId || principal.environment !== this.environment)
      throw new HeimdallError("invalid_argument");
    return this.identity(principal.issuer, principal.subject);
  }
  private lock(identity: Buffer) {
    return createHash("sha256")
      .update(
        `heimdall.identity.v1|${this.productId}|${this.environment}|${identity.toString("hex")}`,
        "ascii",
      )
      .digest()
      .readBigInt64BE()
      .toString();
  }
  private openSession(hash: Buffer, row: Row) {
    try {
      if (row.identity_hash?.length !== 32) throw new Error();
      const record = this.open("session", hash, row);
      if (record && !this.principal(record.value).equals(row.identity_hash)) throw new Error();
      return record;
    } catch {
      throw new HeimdallError("unavailable");
    }
  }
  async createSession(key: string, value: Uint8Array, expiresAt: number, signal?: AbortSignal) {
    return this.writeSession(key, value, expiresAt, undefined, signal);
  }
  async compareAndSwapSession(
    key: string,
    revision: string,
    value: Uint8Array,
    expiresAt: number,
    signal?: AbortSignal,
  ) {
    if (
      typeof revision !== "string" ||
      !/^[1-9][0-9]{0,18}$/.test(revision) ||
      BigInt(revision) > 9223372036854775807n
    )
      throw new HeimdallError("invalid_argument");
    return this.writeSession(key, value, expiresAt, revision, signal);
  }
  private async writeSession(
    key: string,
    input: Uint8Array,
    expiresAt: number,
    revision: string | undefined,
    signal?: AbortSignal,
  ) {
    const value = Buffer.from(input);
    const hash = this.key("session", key);
    const identity = this.principal(value);
    const sealed = this.seal("session", hash, value, expiresAt, identity);
    return transaction(
      this.pool,
      async (client) => {
        await client.query("SELECT pg_advisory_xact_lock($1::bigint)", [this.lock(identity)]);
        const denied = await client.query(
          "SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$3",
          [this.productId, this.environment, identity],
        );
        if (denied.rowCount !== 0) throw new HeimdallError("unauthenticated");
        const args = [
          this.productId,
          this.environment,
          hash,
          sealed.keyId,
          sealed.envelope,
          expiresAt,
          identity,
        ];
        const result =
          revision === undefined
            ? await client.query<Row>(
                `INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at,identity_hash) VALUES($1,$2,'session',$3,$4,$5,$6,$7) RETURNING ${columns}`,
                args,
              )
            : await client.query<Row>(
                `UPDATE heimdall.records SET key_id=$4,envelope=$5,expires_at=$6 WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$3 AND identity_hash=$7 AND revision=$8 AND expires_at>$9 RETURNING ${columns}`,
                [...args, revision, this.now()],
              );
        return result.rows[0] ? this.openSession(hash, result.rows[0]) : null;
      },
      signal,
    );
  }
  async getSession(key: string, signal?: AbortSignal) {
    const hash = this.key("session", key);
    const rows = await query<Row>(
      this.pool,
      `SELECT ${columns} FROM heimdall.records r WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$3 AND identity_hash IS NOT NULL AND expires_at>$4 AND NOT EXISTS(SELECT 1 FROM heimdall.identity_denials d WHERE d.product_id=r.product_id AND d.environment=r.environment AND d.identity_hash=r.identity_hash)`,
      [this.productId, this.environment, hash, this.now()],
      false,
      signal,
    );
    return rows[0] ? this.openSession(hash, rows[0]) : null;
  }
  async deleteSession(key: string, signal?: AbortSignal) {
    const hash = this.key("session", key);
    await query(
      this.pool,
      "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$3 AND identity_hash IS NOT NULL",
      [this.productId, this.environment, hash],
      true,
      signal,
    );
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
      !/^[1-9][0-9]{0,18}$/.test(revision) ||
      BigInt(revision) > 9223372036854775807n
    )
      throw new HeimdallError("invalid_argument");
    const hash = this.key("session", key);
    const identity = this.identity(issuer, subject);
    return transaction(
      this.pool,
      async (client) => {
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
    const rows = await query<{ denied: boolean }>(
      this.pool,
      "SELECT EXISTS(SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$3) AS denied",
      [this.productId, this.environment, identity],
      false,
      signal,
    );
    if (typeof rows[0]?.denied !== "boolean") throw new HeimdallError("unavailable");
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
