import { query } from "./query.js";
import { createHash } from "node:crypto";
import type { Pool } from "pg";
import type { RecordCipher } from "../envelopes.js";
import { HeimdallError } from "../errors.js";

type Kind = "session" | "login";
type Row = { revision: string; envelope: string; expires_at: string; key_id: string };
const token = (s: string, max: number) =>
  typeof s === "string" && s.length > 0 && s.length <= max && !/[^A-Za-z0-9_-]/.test(s);
const columns = "revision::text, envelope, expires_at::text, key_id";

// Internal encrypted record adapter. Caller owns the pool and encryption-key lifecycle.
// Authentication, payload-schema validation and durable audit are composed above this layer.
export class PostgresRecords {
  constructor(
    private readonly pool: Pool,
    private readonly cipher: RecordCipher,
    private readonly productId: string,
    private readonly environment: string,
    private readonly now: () => number = Date.now,
  ) {
    if (!token(productId, 64) || !token(environment, 64))
      throw new HeimdallError("invalid_argument");
  }
  private key(kind: Kind, key: string) {
    if (!["session", "login"].includes(kind) || !token(key, 128))
      throw new HeimdallError("invalid_argument");
    return createHash("sha256").update(key, "ascii").digest();
  }
  private scope(kind: Kind, hash: Buffer) {
    return {
      productId: this.productId,
      environment: this.environment,
      kind,
      recordKey: hash.toString("hex"),
    };
  }
  private seal(kind: Kind, hash: Buffer, value: Uint8Array, expiresAt: number) {
    if (
      !(value instanceof Uint8Array) ||
      value.length > 700000 ||
      !Number.isSafeInteger(expiresAt) ||
      expiresAt <= this.now()
    )
      throw new HeimdallError("invalid_argument");
    const envelope = this.cipher.seal(
      this.scope(kind, hash),
      Buffer.from(JSON.stringify([expiresAt, Buffer.from(value).toString("base64url")])),
    );
    return { envelope, keyId: envelope.split(".")[1]! };
  }
  private open(kind: Kind, hash: Buffer, row: Row) {
    try {
      if (
        !/^[1-9][0-9]*$/.test(row.revision) ||
        BigInt(row.revision) > 9223372036854775807n ||
        row.key_id !== row.envelope.split(".")[1]
      )
        throw new Error();
      const decoded: unknown = JSON.parse(
        Buffer.from(this.cipher.open(this.scope(kind, hash), row.envelope)).toString("utf8"),
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
    const sealed = this.seal(kind, hash, value, expiresAt);
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
      `SELECT ${columns} FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind=$3 AND key_hash=$4 AND expires_at>$5`,
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
    const sealed = this.seal(kind, hash, value, expiresAt);
    const rows = await query<Row>(
      this.pool,
      `UPDATE heimdall.records SET key_id=$5,envelope=$6,expires_at=$7 WHERE product_id=$1 AND environment=$2 AND kind=$3 AND key_hash=$4 AND revision=$8 AND expires_at>$9 RETURNING ${columns}`,
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
      "DELETE FROM heimdall.records WHERE product_id=$1 AND environment=$2 AND kind=$3 AND key_hash=$4",
      [this.productId, this.environment, kind, hash],
      true,
      signal,
    );
  }
}
