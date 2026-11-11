import type { Pool } from "pg";
import { auditDigest, validateDeliveryReceipt } from "../audit-receipts.js";
import { HeimdallError } from "../errors.js";
import { transaction, query } from "./query.js";
const token = (v: unknown, max: number): v is string =>
  typeof v === "string" && v.length > 0 && v.length <= max && !/[^A-Za-z0-9_-]/.test(v);
// Internal persistence port. Factory must supply schema + semantic validation;
// actor verification and event origin remain the authenticated SDK layer's job.
export class PostgresAudit {
  private readonly sinks: string[];
  constructor(
    private readonly pool: Pool,
    private readonly productId: string,
    private readonly environment: string,
    sinks: readonly string[],
    private readonly validate: (event: Record<string, unknown>) => void,
  ) {
    if (
      !token(productId, 64) ||
      !token(environment, 64) ||
      !Array.isArray(sinks) ||
      sinks.length > 32 ||
      sinks.some((s) => !token(s, 128)) ||
      new Set(sinks).size !== sinks.length ||
      typeof validate !== "function"
    )
      throw new HeimdallError("invalid_argument");
    this.sinks = [...sinks].sort();
  }
  private decodeStored(
    wrapper: Record<string, unknown>,
    eventId: string,
    recordedAt: number,
    digest: string,
  ): Record<string, unknown> {
    try {
      if (
        Object.keys(wrapper).length !== 6 ||
        wrapper.format !== "ha1" ||
        wrapper.productId !== this.productId ||
        wrapper.environment !== this.environment ||
        wrapper.eventId !== eventId ||
        wrapper.recordedAt !== recordedAt ||
        typeof wrapper.payload !== "string"
      )
        throw new Error();
      const bytes = Buffer.from(wrapper.payload, "base64url");
      if (bytes.toString("base64url") !== wrapper.payload) throw new Error();
      const decoded = JSON.parse(bytes.toString("utf8")) as Record<string, unknown>;
      this.validate(decoded);
      if (
        decoded.productId !== this.productId ||
        decoded.environment !== this.environment ||
        decoded.eventId !== eventId ||
        decoded.recordedAt !== recordedAt ||
        auditDigest(decoded) !== digest
      )
        throw new Error();
      return decoded;
    } catch {
      throw new HeimdallError("audit_unavailable");
    }
  }
  // A read is not a claim/lease. Concurrent workers may select the same records.
  async pending(sink: string, now: number, limit: number, signal?: AbortSignal) {
    if (
      !token(sink, 128) ||
      !Number.isSafeInteger(now) ||
      now < 0 ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100
    )
      throw new HeimdallError("invalid_argument");
    const rows = await query<{
      event_id: string;
      attempts: number;
      recorded_at: string;
      payload_digest: Buffer;
      event: Record<string, unknown>;
    }>(
      this.pool,
      `WITH candidates AS MATERIALIZED (
      SELECT d.event_id,d.attempts,d.next_attempt_at,e.recorded_at,e.payload_digest,e.event
      FROM heimdall.audit_deliveries d JOIN heimdall.audit_events e USING(product_id,environment,event_id)
      WHERE d.product_id=$1 AND d.environment=$2 AND d.sink_id=$3 AND d.state='pending' AND d.next_attempt_at<=$4
      ORDER BY d.next_attempt_at,d.event_id COLLATE "C" LIMIT $5
    ), sized AS (
      SELECT *,sum(octet_length(event::text)) OVER(ORDER BY next_attempt_at,event_id COLLATE "C") AS size FROM candidates
    ) SELECT event_id,attempts,recorded_at::text,payload_digest,event FROM sized WHERE size<=4194304 ORDER BY next_attempt_at,event_id COLLATE "C"`,
      [this.productId, this.environment, sink, now, limit],
      false,
      signal,
    );
    return rows.map((row) => {
      let event: Record<string, unknown> | null = null;
      try {
        event = this.decodeStored(
          row.event,
          row.event_id,
          Number(row.recorded_at),
          row.payload_digest.toString("hex"),
        );
      } catch {
        /* Return explicit invalid record identity for quarantine; never export its payload. */
      }
      return { eventId: row.event_id, attempts: row.attempts, event };
    });
  }
  async append(event: Record<string, unknown>, signal?: AbortSignal) {
    // Snapshot before the first await; never let caller mutation change dispatched data.
    const originalDigest = auditDigest(event);
    const encoded = JSON.stringify(event);
    const snapshot = JSON.parse(encoded) as Record<string, unknown>;
    this.validate(snapshot);
    if (
      auditDigest(snapshot) !== originalDigest ||
      snapshot.productId !== this.productId ||
      snapshot.environment !== this.environment ||
      !token(snapshot.eventId, 128) ||
      !Number.isSafeInteger(snapshot.recordedAt) ||
      Number(snapshot.recordedAt) < 0
    )
      throw new HeimdallError("invalid_argument");
    const eventId = snapshot.eventId;
    const recordedAt = snapshot.recordedAt as number;
    const storedEnvelope = JSON.stringify({
      format: "ha1",
      productId: this.productId,
      environment: this.environment,
      eventId,
      recordedAt,
      payload: Buffer.from(encoded, "utf8").toString("base64url"),
    });
    return transaction(
      this.pool,
      async (client) => {
        const parameters = [this.productId, this.environment, eventId];
        const inserted = await client.query(
          "INSERT INTO heimdall.audit_events(product_id,environment,event_id,payload_digest,recorded_at,event) VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT DO NOTHING RETURNING event_id",
          [...parameters, Buffer.from(originalDigest, "hex"), recordedAt, storedEnvelope],
        );
        const existing = await client.query<{
          payload_digest: Buffer;
          recorded_at: string;
          event: Record<string, unknown>;
        }>(
          "SELECT payload_digest,recorded_at::text,event FROM heimdall.audit_events WHERE product_id=$1 AND environment=$2 AND event_id=$3",
          parameters,
        );
        const row = existing.rows[0];
        if (!row) throw new HeimdallError("audit_unavailable");
        if (row.payload_digest.toString("hex") !== originalDigest)
          throw new HeimdallError("conflict");
        if (row.recorded_at !== String(recordedAt)) throw new HeimdallError("audit_unavailable");
        this.decodeStored(row.event, eventId, recordedAt, originalDigest);
        if (inserted.rowCount === 1) {
          for (const sink of this.sinks)
            await client.query(
              "INSERT INTO heimdall.audit_deliveries(product_id,environment,event_id,sink_id) VALUES($1,$2,$3,$4)",
              [...parameters, sink],
            );
        }
        return {
          productId: this.productId,
          environment: this.environment,
          eventId,
          recordedAt,
          payloadDigest: originalDigest,
        };
      },
      signal,
    );
  }
  // batch is the exact submitted batch; receipt has already passed authenticated
  // transport/schema validation. deliveredAt is the local acknowledgement time.
  async acknowledge(
    batch: Parameters<typeof validateDeliveryReceipt>[0],
    receipt: Parameters<typeof validateDeliveryReceipt>[1],
    deliveredAt: number,
    signal?: AbortSignal,
  ): Promise<void> {
    const shape = (value: typeof batch) =>
      token(value.productId, 64) &&
      token(value.environment, 64) &&
      token(value.sinkId, 128) &&
      Array.isArray(value.eventIds) &&
      value.eventIds.length > 0 &&
      value.eventIds.length <= 1000 &&
      value.eventIds.every((id) => token(id, 128)) &&
      new Set(value.eventIds).size === value.eventIds.length;
    if (
      !shape(batch) ||
      batch.productId !== this.productId ||
      batch.environment !== this.environment ||
      !Number.isSafeInteger(deliveredAt) ||
      deliveredAt < 0
    )
      throw new HeimdallError("invalid_argument");
    if (!shape(receipt)) throw new HeimdallError("unavailable");
    validateDeliveryReceipt(batch, receipt);
    const ids = [...receipt.eventIds];
    const sink = batch.sinkId;
    await transaction(
      this.pool,
      async (client) => {
        const parameters = [this.productId, this.environment, sink, ids];
        const rows = await client.query<{ state: string }>(
          "SELECT state FROM heimdall.audit_deliveries WHERE product_id=$1 AND environment=$2 AND sink_id=$3 AND event_id=ANY($4::text[]) ORDER BY event_id FOR UPDATE",
          parameters,
        );
        if (
          rows.rows.length !== ids.length ||
          rows.rows.some((row) => row.state !== "pending" && row.state !== "delivered")
        )
          throw new HeimdallError("unavailable");
        const updated = await client.query(
          "UPDATE heimdall.audit_deliveries SET state='delivered',delivered_at=$5 WHERE product_id=$1 AND environment=$2 AND sink_id=$3 AND event_id=ANY($4::text[]) AND state='pending'",
          [...parameters, deliveredAt],
        );
        if (updated.rowCount !== rows.rows.filter((row) => row.state === "pending").length)
          throw new HeimdallError("unavailable");
      },
      signal,
    );
  }
  private deliveryArgs(sink: string, eventId: string, attempts: number) {
    if (
      !token(sink, 128) ||
      !token(eventId, 128) ||
      !Number.isInteger(attempts) ||
      attempts < 0 ||
      attempts > 2147483647
    )
      throw new HeimdallError("invalid_argument");
    return [this.productId, this.environment, sink, eventId, attempts];
  }
  // Claims a delivery attempt, not exclusive ownership of an external request.
  async beginAttempt(
    sink: string,
    eventId: string,
    expectedAttempts: number,
    now: number,
    retryAt: number,
    signal?: AbortSignal,
  ): Promise<number | null> {
    const args = this.deliveryArgs(sink, eventId, expectedAttempts);
    if (
      expectedAttempts === 2147483647 ||
      !Number.isSafeInteger(now) ||
      now < 0 ||
      !Number.isSafeInteger(retryAt) ||
      retryAt <= now
    )
      throw new HeimdallError("invalid_argument");
    return transaction(
      this.pool,
      async (client) => {
        const rows = await client.query<{ attempts: number }>(
          "UPDATE heimdall.audit_deliveries SET attempts=attempts+1,next_attempt_at=$7 WHERE product_id=$1 AND environment=$2 AND sink_id=$3 AND event_id=$4 AND attempts=$5 AND state='pending' AND next_attempt_at<=$6 RETURNING attempts",
          [...args, now, retryAt],
        );
        return rows.rows[0]?.attempts ?? null;
      },
      signal,
    );
  }
  async deferAttempt(
    sink: string,
    eventId: string,
    attempt: number,
    retryAt: number,
    signal?: AbortSignal,
  ): Promise<boolean> {
    const args = this.deliveryArgs(sink, eventId, attempt);
    if (attempt < 1 || !Number.isSafeInteger(retryAt) || retryAt < 0)
      throw new HeimdallError("invalid_argument");
    return transaction(
      this.pool,
      async (client) => {
        const rows = await client.query(
          "UPDATE heimdall.audit_deliveries SET next_attempt_at=GREATEST(next_attempt_at,$6) WHERE product_id=$1 AND environment=$2 AND sink_id=$3 AND event_id=$4 AND attempts=$5 AND state='pending' RETURNING attempts",
          [...args, retryAt],
        );
        return rows.rowCount === 1;
      },
      signal,
    );
  }
  async quarantine(
    sink: string,
    eventId: string,
    expectedAttempts: number,
    signal?: AbortSignal,
  ): Promise<boolean> {
    const args = this.deliveryArgs(sink, eventId, expectedAttempts);
    return transaction(
      this.pool,
      async (client) => {
        const rows = await client.query(
          "UPDATE heimdall.audit_deliveries SET state='quarantined' WHERE product_id=$1 AND environment=$2 AND sink_id=$3 AND event_id=$4 AND attempts=$5 AND state IN ('pending','quarantined') RETURNING attempts",
          args,
        );
        return rows.rowCount === 1;
      },
      signal,
    );
  }
}
