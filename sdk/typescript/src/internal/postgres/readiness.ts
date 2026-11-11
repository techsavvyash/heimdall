import type { Pool } from "pg";
import { HeimdallError } from "../errors.js";
import { query } from "./query.js";
// Bundled v1 policy. A changed catalog requires explicit protocol review and tests.
const checksum = "f71db3eece51c3230c27c89a4e154bda18e15de3276b2e7eb0001a3e38c79cc4";
// One statement observes metadata and ledger from the same PostgreSQL snapshot.
const sql = `SELECT NOT pg_is_in_recovery()
 AND current_setting('transaction_read_only') = 'off'
 AND (SELECT count(*) FROM heimdall.schema_metadata) = 1
 AND (SELECT count(*) FROM heimdall.schema_metadata WHERE singleton = true
   AND database_version = 1 AND state = 'ready'
   AND reader_min = 1 AND reader_max BETWEEN 1 AND 1000
   AND writer_min = 1 AND writer_max BETWEEN 1 AND 1000) = 1
 AND (SELECT count(*) FROM heimdall.migrations) = 1
 AND (SELECT count(*) FROM heimdall.migrations WHERE version = 1 AND checksum = $1) = 1 AS ready`;

// Read-only startup check; never infers initialization permission from missing tables.
export async function checkReadiness(pool: Pool, signal?: AbortSignal): Promise<void> {
  const rows = await query<{ ready: boolean }>(pool, sql, [checksum], false, signal);
  if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
}
