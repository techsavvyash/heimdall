import { query } from "./query.js";
import { createHash } from "node:crypto";
import type { Pool } from "pg";
import type { RecordCipher } from "../envelopes.js";
import { reserveRecordKeyUsage } from "./key-usage.js";
import { HeimdallError, checkCancellation } from "../errors.js";

type Kind = "session" | "login";
export type Row = {
  revision: string;
  envelope: string;
  expires_at: string;
  key_id: string;
  identity_hash: Buffer | null;
};
const token = (s: string, max: number) =>
  typeof s === "string" && s.length > 0 && s.length <= max && !/[^A-Za-z0-9_-]/.test(s);
export const columns = "revision::text, envelope, expires_at::text, key_id, identity_hash";

// Internal encrypted record adapter. Caller owns the pool and encryption-key lifecycle.
// Authentication, payload-schema validation and durable audit are composed above this layer.
export class PostgresRecords {
  constructor(
    protected readonly pool: Pool,
    protected readonly keyUsagePool: Pool,
    protected readonly cipher: RecordCipher,
    protected readonly productId: string,
    protected readonly environment: string,
    protected readonly now: () => number = Date.now,
  ) {
    if (!token(productId, 64) || !token(environment, 64))
      throw new HeimdallError("invalid_argument");
  }
  protected key(kind: Kind, key: string) {
    if (!["session", "login"].includes(kind) || !token(key, 128))
      throw new HeimdallError("invalid_argument");
    return createHash("sha256").update(key, "ascii").digest();
  }
  protected scope(kind: Kind, hash: Buffer, identityHash?: Buffer | null) {
    return {
      productId: this.productId,
      environment: this.environment,
      kind,
      recordKey: hash.toString("hex") + (identityHash?.toString("hex") ?? ""),
    };
  }
  protected async seal(
    kind: Kind,
    hash: Buffer,
    value: Uint8Array,
    expiresAt: number,
    identityHash?: Buffer,
    signal?: AbortSignal,
  ) {
    if (
      !(value instanceof Uint8Array) ||
      value.length > 700000 ||
      !Number.isSafeInteger(expiresAt) ||
      expiresAt <= this.now()
    )
      throw new HeimdallError("invalid_argument");
    const scope = this.scope(kind, hash, identityHash);
    const plaintext = Buffer.from(
      JSON.stringify([expiresAt, Buffer.from(value).toString("base64url")]),
    );
    await reserveRecordKeyUsage(this.keyUsagePool, this.cipher.activeFingerprint(), signal);
    if (signal) checkCancellation(signal);
    const envelope = this.cipher.seal(scope, plaintext);
    return { envelope, keyId: envelope.split(".")[1]! };
  }
  protected open(kind: Kind, hash: Buffer, row: Row) {
    try {
      if (row.identity_hash !== null && row.identity_hash.length !== 32) throw new Error();
      if (
        !/^[1-9][0-9]*$/.test(row.revision) ||
        BigInt(row.revision) > 9223372036854775807n ||
        row.key_id !== row.envelope.split(".")[1]
      )
        throw new Error();
      const decoded: unknown = JSON.parse(
        Buffer.from(
          this.cipher.open(this.scope(kind, hash, row.identity_hash), row.envelope),
        ).toString("utf8"),
      );
      if (
        !Array.isArray(decoded) ||
        decoded.length !== 2 ||
        !Number.isSafeInteger(decoded[0]) ||
        String(decoded[0]) !== row.expires_at ||
        typeof decoded[1] !== "string" ||
        !/^[A-Za-z0-9_-]*$/.test(decoded[1])
      )
        throw new Error();
      const value = Buffer.from(decoded[1], "base64url");
      if (value.toString("base64url") !== decoded[1]) throw new Error();
      return decoded[0] <= this.now()
        ? null
        : { revision: row.revision, expiresAt: decoded[0] as number, value };
    } catch {
      throw new HeimdallError("unavailable");
    }
  }
  async create(
    kind: Kind,
    key: string,
    value: Uint8Array,
    expiresAt: number,
    signal?: AbortSignal,
  ) {
    const hash = this.key(kind, key);
    const sealed = await this.seal(kind, hash, value, expiresAt, undefined, signal);
    const rows = await query<Row>(
      this.pool,
      `INSERT INTO heimdall.records(product_id,environment,kind,key_hash,key_id,envelope,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING ${columns}`,
      [this.productId, this.environment, kind, hash, sealed.keyId, sealed.envelope, expiresAt],
      true,
      signal,
    );
    return this.open(kind, hash, rows[0]!);
  }
  async get(kind: Kind, key: string, signal?: AbortSignal) {
    const hash = this.key(kind, key);
    const rows = await query<Row>(
      this.pool,
      `SELECT ${columns} FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind=$3 AND key_hash=$4 AND identity_hash IS NULL AND expires_at>$5`,
      [this.productId, this.environment, kind, hash, this.now()],
      false,
      signal,
    );
    return rows[0] ? this.open(kind, hash, rows[0]) : null;
  }
  async compareAndSwap(
    kind: Kind,
    key: string,
    revision: string,
    value: Uint8Array,
    expiresAt: number,
    signal?: AbortSignal,
  ) {
    if (!/^[1-9][0-9]{0,18}$/.test(revision) || BigInt(revision) > 9223372036854775807n)
      throw new HeimdallError("invalid_argument");
    const hash = this.key(kind, key);
    const sealed = await this.seal(kind, hash, value, expiresAt, undefined, signal);
    const rows = await query<Row>(
      this.pool,
      `UPDATE heimdall.records SET key_id=$5,envelope=$6,expires_at=$7 WHERE product_id=$1 AND environment=$2 AND kind=$3 AND key_hash=$4 AND identity_hash IS NULL AND revision=$8 AND expires_at>$9 RETURNING ${columns}`,
      [
        this.productId,
        this.environment,
        kind,
        hash,
        sealed.keyId,
        sealed.envelope,
        expiresAt,
        revision,
        this.now(),
      ],
      true,
      signal,
    );
    return rows[0] ? this.open(kind, hash, rows[0]) : null;
  }
  async delete(kind: Kind, key: string, signal?: AbortSignal): Promise<void> {
    const hash = this.key(kind, key);
    await query<Row>(
      this.pool,
      "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind=$3 AND key_hash=$4 AND identity_hash IS NULL",
      [this.productId, this.environment, kind, hash],
      true,
      signal,
    );
  }
}
