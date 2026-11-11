import type { PoolClient } from "pg";
import { HeimdallError } from "../errors.js";

// Prepared internal transaction helpers. Deployment must account all history and
// fence legacy writers before a future public protocol can use this ledger.
export async function lockAuditCapacity(client: PoolClient) {
  await client.query("SELECT pg_advisory_xact_lock_shared(74162401)");
  const result = await client.query(
    "SELECT state FROM heimdall_capacity.policy WHERE singleton AND NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' FOR UPDATE",
  );
  if (result.rows.length !== 1 || result.rows[0].state !== "ready")
    throw new HeimdallError("audit_unavailable");
}
async function capacityWrite(client: PoolClient, sql: string, args: unknown[]) {
  try {
    return await client.query(sql, args);
  } catch (error) {
    if (
      typeof error === "object" &&
      error !== null &&
      "code" in error &&
      ["23514", "23503", "P0002"].includes(String(error.code))
    )
      throw new HeimdallError("audit_unavailable");
    throw error;
  }
}
export async function chargeAuditEvent(
  client: PoolClient,
  scope: string[],
  eventId: string,
  inserted: boolean,
) {
  if (inserted)
    await capacityWrite(
      client,
      "INSERT INTO heimdall_capacity.event_charges(product_id,environment,event_id) VALUES($1,$2,$3)",
      [...scope, eventId],
    );
  // An existing uncharged event is not a retry receipt. Only offline backfill may
  // account historical records; runtime must not silently repair incomplete state.
  const checked = await client.query(
    `SELECT EXISTS(
    SELECT 1 FROM heimdall_capacity.event_charges c JOIN heimdall.audit_events e USING(product_id,environment,event_id)
    WHERE c.product_id=$1 AND c.environment=$2 AND c.event_id=$3
    AND c.envelope_bytes=octet_length(e.event::text)
    AND c.delivery_rows=(SELECT count(*) FROM heimdall.audit_deliveries d WHERE d.product_id=c.product_id AND d.environment=c.environment AND d.event_id=c.event_id)
  ) AS valid`,
    [...scope, eventId],
  );
  if (checked.rows[0]?.valid !== true) throw new HeimdallError("audit_unavailable");
}
export async function reserveAuditDecision(
  client: PoolClient,
  scope: string[],
  event: Record<string, unknown>,
  inserted: boolean,
) {
  if (inserted)
    await capacityWrite(
      client,
      "INSERT INTO heimdall_capacity.reservations(product_id,environment,decision_id,request_event_id) VALUES($1,$2,$3,$4)",
      [...scope, event.decisionId, event.eventId],
    );
}
export async function resolveAuditCapacity(
  client: PoolClient,
  scope: string[],
  event: Record<string, unknown>,
  changed: boolean,
  recoveryId: string | null,
) {
  if (!changed) return;
  const result = await capacityWrite(
    client,
    "UPDATE heimdall_capacity.reservations SET resolution=$4,terminal_event_id=$5,recovery_event_id=$6 WHERE product_id=$1 AND environment=$2 AND decision_id=$3 AND resolution='pending'",
    [
      ...scope,
      event.decisionId,
      recoveryId === null ? "completed" : "recovered",
      event.eventId,
      recoveryId,
    ],
  );
  if (result.rowCount !== 1) throw new HeimdallError("audit_unavailable");
}
export async function checkAuditReservation(
  client: PoolClient,
  scope: string[],
  event: Record<string, unknown>,
  recoveryId: string | null,
) {
  const request = event.eventType === "heimdall.authorization.requested";
  const result = await client.query(
    `SELECT EXISTS(
    SELECT 1 FROM heimdall_capacity.reservations r JOIN heimdall.audit_decisions d USING(product_id,environment,decision_id)
    JOIN heimdall_capacity.event_charges c ON c.product_id=r.product_id AND c.environment=r.environment AND c.event_id=r.request_event_id
    WHERE r.product_id=$1 AND r.environment=$2 AND r.decision_id=$3
    AND r.request_event_id=d.request_event_id AND r.resolution=d.resolution
    AND r.terminal_event_id IS NOT DISTINCT FROM d.terminal_event_id
    AND (($4 AND r.request_event_id=$5) OR (NOT $4 AND r.terminal_event_id=$5 AND r.recovery_event_id IS NOT DISTINCT FROM $6::text))
  ) AS valid`,
    [...scope, event.decisionId, request, event.eventId, recoveryId],
  );
  if (result.rows[0]?.valid !== true) throw new HeimdallError("audit_unavailable");
}
