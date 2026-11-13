import { IdentityRecords } from "./identity.js";
import type { Pool } from "pg";
import type { RecordCipher } from "../envelopes.js";
import { HeimdallError } from "../errors.js";
import { PostgresRecords, columns } from "./records.js";
import type { Row } from "./records.js";
import { query, transaction } from "./query.js";
type Principal = { productId: string; environment: string; issuer: string; subject: string };
// Validation includes the session schema and returns its exact principal/scope.
// Provider authentication and freshness remain the calling SDK layer's job.
export class SessionRecords extends PostgresRecords {
  private readonly identities: IdentityRecords;
  readonly identityGate: IdentityRecords["identityGate"];
  readonly invalidateSessionVersion: IdentityRecords["invalidateSessionVersion"];
  readonly isIdentityDenied: IdentityRecords["isIdentityDenied"];
  readonly invalidateIdentity: IdentityRecords["invalidateIdentity"];
  constructor(
    pool: Pool,
    keyUsagePool: Pool,
    cipher: RecordCipher,
    productId: string,
    environment: string,
    private readonly validate: (value: Uint8Array) => Principal,
    now: () => number = Date.now,
  ) {
    super(pool, keyUsagePool, cipher, productId, environment, now);
    this.identities = new IdentityRecords(pool, productId, environment, now);
    this.identityGate = this.identities.identityGate.bind(this.identities);
    this.invalidateSessionVersion = this.identities.invalidateSessionVersion.bind(this.identities);
    this.isIdentityDenied = this.identities.isIdentityDenied.bind(this.identities);
    this.invalidateIdentity = this.identities.invalidateIdentity.bind(this.identities);
    if (typeof validate !== "function") throw new HeimdallError("invalid_argument");
  }
  private principal(value: Uint8Array) {
    const principal = this.validate(value);
    if (principal.productId !== this.productId || principal.environment !== this.environment)
      throw new HeimdallError("invalid_argument");
    return this.identities.identity(principal.issuer, principal.subject);
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
    this.validateRevision(revision);
    return this.writeSession(key, value, expiresAt, { revision }, signal);
  }
  async replaceSession(
    previousKey: string,
    previousRevision: string,
    key: string,
    value: Uint8Array,
    expiresAt: number,
    signal?: AbortSignal,
  ) {
    this.validateRevision(previousRevision);
    const previousHash = this.key("session", previousKey);
    if (previousKey === key) throw new HeimdallError("invalid_argument");
    return this.writeSession(
      key,
      value,
      expiresAt,
      { revision: previousRevision, previousHash },
      signal,
    );
  }
  private validateRevision(revision: string) {
    if (
      typeof revision !== "string" ||
      !/^[1-9][0-9]{0,18}(?![\s\S])/.test(revision) ||
      BigInt(revision) > 9223372036854775807n
    )
      throw new HeimdallError("invalid_argument");
  }
  private async writeSession(
    key: string,
    input: Uint8Array,
    expiresAt: number,
    existing: { revision: string; previousHash?: Buffer } | undefined,
    signal?: AbortSignal,
  ) {
    const value = Buffer.from(input);
    const hash = this.key("session", key);
    const identity = this.principal(value);
    const sealed = await this.seal("session", hash, value, expiresAt, identity, signal);
    return transaction(
      this.pool,
      async (client) => {
        await client.query("SELECT pg_advisory_xact_lock($1::bigint)", [
          this.identities.lock(identity),
        ]);
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
        let result;
        if (!existing) {
          result = await client.query<Row>(
            `INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at,identity_hash) VALUES($1,$2,'session',$3,$4,$5,$6,$7) RETURNING ${columns}`,
            args,
          );
        } else if (existing.previousHash) {
          result = await client.query<Row>(
            `UPDATE heimdall.records SET key_hash=$3,key_id=$4,envelope=$5,expires_at=$6,identity_hash=$7
              WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$10
                AND identity_hash IS NOT NULL AND revision=$8 AND expires_at>$9 RETURNING ${columns}`,
            [...args, existing.revision, this.now(), existing.previousHash],
          );
        } else {
          result = await client.query<Row>(
            `UPDATE heimdall.records SET key_id=$4,envelope=$5,expires_at=$6 WHERE product_id=$1 AND environment=$2 AND kind='session' AND key_hash=$3 AND identity_hash=$7 AND revision=$8 AND expires_at>$9 RETURNING ${columns}`,
            [...args, existing.revision, this.now()],
          );
        }
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
}
