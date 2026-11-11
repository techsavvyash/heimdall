import type { Pool } from "pg";
import { HeimdallError } from "../errors.js";
import { validateKeyUsage } from "../key-usage.js";
import { transaction } from "./query.js";
import { keyUsageChecksum, keyUsageSQL } from "./readiness.js";

// Each successful call burns one allowance, even if later encryption or storage fails.
// Never encrypt after a rejected call, including an uncertain COMMIT response.
export async function reserveRecordKeyUsage(
  pool: Pool,
  fingerprint: string,
  signal?: AbortSignal,
): Promise<void> {
  return changeRecordKeyUsage(pool, fingerprint, "reserve", 1, signal);
}

export async function registerRecordKeyUsage(
  pool: Pool,
  fingerprint: string,
  limit: number,
  freshKey: boolean,
  signal?: AbortSignal,
): Promise<void> {
  if (freshKey !== true) throw new HeimdallError("invalid_argument");
  return changeRecordKeyUsage(pool, fingerprint, "register", limit, signal);
}
export async function retireRecordKeyUsage(
  pool: Pool,
  fingerprint: string,
  signal?: AbortSignal,
): Promise<void> {
  return changeRecordKeyUsage(pool, fingerprint, "retire", 1, signal);
}
async function changeRecordKeyUsage(
  pool: Pool,
  fingerprint: string,
  operation: "reserve" | "register" | "retire",
  limit: number,
  signal?: AbortSignal,
): Promise<void> {
  validateKeyUsage(fingerprint, limit);
  await transaction(
    pool,
    async (client) => {
      await client.query("SET LOCAL lock_timeout='4s'; SET LOCAL statement_timeout='4s'");
      await client.query("SELECT pg_advisory_xact_lock_shared(74162401)");
      const readiness = await client.query<{ ready: boolean }>(keyUsageSQL, [keyUsageChecksum]);
      if (readiness.rows.length !== 1 || readiness.rows[0]?.ready !== true)
        throw new HeimdallError("unavailable");
      if (operation === "register") {
        await client.query(
          "INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit) VALUES(decode($1,'hex'),$2) ON CONFLICT(fingerprint) DO NOTHING",
          [fingerprint, limit],
        );
        const { rows } = await client.query<{ encryption_limit: string; state: string }>(
          "SELECT encryption_limit,state FROM heimdall_keys.record_key_usage WHERE fingerprint=decode($1,'hex') FOR UPDATE",
          [fingerprint],
        );
        if (rows.length !== 1) throw new HeimdallError("unavailable");
        if (Number(rows[0]!.encryption_limit) !== limit || rows[0]!.state !== "active")
          throw new HeimdallError("conflict");
      } else {
        const sql =
          operation === "retire"
            ? "UPDATE heimdall_keys.record_key_usage SET state='retired' WHERE fingerprint=decode($1,'hex') RETURNING state"
            : "UPDATE heimdall_keys.record_key_usage SET used=used+1 WHERE fingerprint=decode($1,'hex') AND state='active' AND used<encryption_limit RETURNING used";
        const result = await client.query(sql, [fingerprint]);
        if (result.rows.length !== 1) throw new HeimdallError("unavailable");
      }
    },
    signal,
  );
}
