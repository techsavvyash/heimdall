import type { Pool } from "pg";
import { HeimdallError } from "../errors.js";
import { query } from "./query.js";
// Legacy v1 policy; activated v2 uses the separate checker below.
export const sessionChecksum = "f71db3eece51c3230c27c89a4e154bda18e15de3276b2e7eb0001a3e38c79cc4";
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
  const rows = await query<{ ready: boolean }>(pool, sql, [sessionChecksum], false, signal);
  if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
}

const managedChecksum = "3df4dc62f6271691c56ac605c51012a97301b98c70454af62984cf253c066c55";
// Both schema families are observed in one statement snapshot, never two pool reads.
const managedSQL = `SELECT base.ready AND managed.ready AS ready FROM (${sql}) base CROSS JOIN (${sql.replaceAll("heimdall.", "heimdall_managed.").replaceAll("$1", "$2")}) managed`;
export async function checkManagedReadiness(pool: Pool, signal?: AbortSignal): Promise<void> {
  const rows = await query<{ ready: boolean }>(
    pool,
    managedSQL,
    [sessionChecksum, managedChecksum],
    false,
    signal,
  );
  if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
}

// Activated decision-aware protocol. Kept separate from legacy v1 readers.
export const decisionChecksum = "f7e859c78a6d099ad4a4d35c96e553ba10a53e3ae7e23440237c769efcd50722";
const auditSQL = `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'
 AND (SELECT count(*) FROM heimdall.schema_metadata)=1
 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=2 AND state='ready' AND reader_min=2 AND reader_max=2 AND writer_min=2 AND writer_max=2)
 AND (SELECT count(*) FROM heimdall.migrations)=2
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=1 AND checksum=$1)
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=2 AND checksum=$2)
 AND to_regclass('heimdall.audit_decisions') IS NOT NULL AS ready`;
export async function checkAuditReadiness(pool: Pool, signal?: AbortSignal): Promise<void> {
  const rows = await query<{ ready: boolean }>(
    pool,
    auditSQL,
    [sessionChecksum, decisionChecksum],
    false,
    signal,
  );
  if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
}

const managedAuditSQL = `SELECT base.ready AND managed.ready AS ready FROM (${auditSQL}) base CROSS JOIN (${sql.replaceAll("heimdall.", "heimdall_managed.").replaceAll("$1", "$3")}) managed`;
export async function checkManagedAuditReadiness(pool: Pool, signal?: AbortSignal): Promise<void> {
  const rows = await query<{ ready: boolean }>(
    pool,
    managedAuditSQL,
    [sessionChecksum, decisionChecksum, managedChecksum],
    false,
    signal,
  );
  if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
}

export const keyUsageChecksum = "eb47f12e3a390f450d63fea50e8da47708e5e473c78bb14761b56cfdcc799bef";
export const keyUsageSQL = sql.replaceAll("heimdall.", "heimdall_keys.");
export async function checkKeyUsageReadiness(pool: Pool, signal?: AbortSignal): Promise<void> {
  const rows = await query<{ ready: boolean }>(
    pool,
    keyUsageSQL,
    [keyUsageChecksum],
    false,
    signal,
  );
  if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
}

export const capacityChecksum = "15da3b2fa8a64b285516dc554a9c1cb7268fec5427046722ab9854cc0710e492";
export const capacityPreparationChecksum =
  "60366da3390b8255dcabc6d1308a4fdfe22f012ee2e7e93a4434afb19c40ab86";
export const capacitySQL = `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'
 AND (SELECT count(*) FROM heimdall.schema_metadata)=1
 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=3 AND state='ready' AND reader_min=3 AND reader_max=3 AND writer_min=3 AND writer_max=3)
 AND (SELECT count(*) FROM heimdall.migrations)=3
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=1 AND checksum=$1)
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=2 AND checksum=$2)
 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=3 AND checksum=$3)
 AND (SELECT count(*) FROM heimdall_capacity.policy)=1
 AND (SELECT count(*) FROM heimdall_capacity.upgrade)=1
 AND (SELECT count(*) FROM heimdall_capacity.backfill)=1
 AND EXISTS(SELECT 1 FROM heimdall_capacity.backfill WHERE singleton AND phase='complete' AND after_product IS NULL AND after_environment IS NULL AND after_id IS NULL)
 AND EXISTS(SELECT 1 FROM heimdall_capacity.policy p JOIN heimdall_capacity.upgrade u USING(singleton) WHERE p.singleton AND p.state='ready' AND u.capacity_checksum=$3 AND u.preparation_checksum=$4 AND p.event_limit=u.event_limit AND p.byte_limit=u.byte_limit AND p.delivery_limit=u.delivery_limit) AS ready`;
export async function checkCapacityReadiness(pool: Pool, signal?: AbortSignal): Promise<void> {
  const rows = await query<{ ready: boolean }>(
    pool,
    capacitySQL,
    [sessionChecksum, decisionChecksum, capacityChecksum, capacityPreparationChecksum],
    false,
    signal,
  );
  if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
}
const managedCapacitySQL = `SELECT base.ready AND managed.ready AS ready FROM (${capacitySQL}) base CROSS JOIN (${sql.replaceAll("heimdall.", "heimdall_managed.").replaceAll("$1", "$5")}) managed`;
export async function checkManagedCapacityReadiness(
  pool: Pool,
  signal?: AbortSignal,
): Promise<void> {
  const rows = await query<{ ready: boolean }>(
    pool,
    managedCapacitySQL,
    [
      sessionChecksum,
      decisionChecksum,
      capacityChecksum,
      capacityPreparationChecksum,
      managedChecksum,
    ],
    false,
    signal,
  );
  if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
}
