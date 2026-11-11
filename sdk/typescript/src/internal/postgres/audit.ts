import { validateRecoveryPair } from "../audit-recovery.js";
import { sessionChecksum } from "./readiness.js";
import { bindAuditDecision } from "../audit-decision-binding.js";
import type { Pool, PoolClient } from "pg";
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
  // Deployment-only: old writers must be drained and the explicit upgrade must
  // install both prepared v2 tables and register every audit scope first.
  async backfillDecisionPage(limit: number, signal?: AbortSignal) {
    if (!Number.isInteger(limit) || limit < 1 || limit > 100)
      throw new HeimdallError("invalid_argument");
    return transaction(
      this.pool,
      async (client) => {
        await client.query("SELECT pg_advisory_xact_lock(74162401)");
        const gate = await client.query(
          "SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AND (SELECT count(*) FROM heimdall.schema_metadata)=1 AND EXISTS(SELECT 1 FROM heimdall.schema_metadata WHERE singleton AND database_version=1 AND state='migrating') AND (SELECT count(*) FROM heimdall.migrations)=1 AND EXISTS(SELECT 1 FROM heimdall.migrations WHERE version=1 AND checksum=$1) AS permitted",
          [sessionChecksum],
        );
        if (gate.rows[0]?.permitted !== true) throw new HeimdallError("unavailable");
        const progress = await client.query(
          "SELECT phase,last_event_id,scanned_count::text,expected_count::text FROM heimdall.audit_decision_backfill WHERE product_id=$1 AND environment=$2 FOR UPDATE",
          [this.productId, this.environment],
        );
        if (progress.rows.length !== 1) throw new HeimdallError("unavailable");
        const p = progress.rows[0]!;
        const integer = (v: unknown): number => {
          if (
            typeof v !== "string" ||
            !/^(0|[1-9][0-9]{0,15})(?![\s\S])/.test(v) ||
            !Number.isSafeInteger(Number(v))
          )
            throw new HeimdallError("audit_unavailable");
          return Number(v);
        };
        let count = integer(p.scanned_count);
        let expected = p.expected_count === null ? null : integer(p.expected_count);
        let phase = p.phase as string;
        let after = p.last_event_id as string | null;
        if (
          !["requests", "outcomes", "complete"].includes(phase) ||
          (after !== null && !token(after, 128)) ||
          (phase === "requests") !== (expected === null) ||
          (phase === "complete" && (after !== null || count !== expected))
        )
          throw new HeimdallError("audit_unavailable");
        if (phase !== "complete" && (after === null) !== (count === 0))
          throw new HeimdallError("audit_unavailable");
        if (phase === "complete")
          return { phase, after, scannedCount: count, expectedCount: expected, processed: 0 };
        const page = await client.query<{
          total: number;
          records: {
            event_id: string;
            recorded_at: string;
            payload_digest: string;
            event: Record<string, unknown>;
          }[];
        }>(
          `WITH selected AS MATERIALIZED (
 SELECT event_id,recorded_at,payload_digest,event FROM heimdall.audit_events
 WHERE product_id=$1 AND environment=$2 AND ($3::text IS NULL OR event_id COLLATE "C">$3 COLLATE "C") ORDER BY event_id COLLATE "C" LIMIT $4
), sized AS (SELECT *,sum(octet_length(event::text)) OVER(ORDER BY event_id COLLATE "C") AS size FROM selected)
 SELECT (SELECT count(*)::int FROM selected) AS total,COALESCE(jsonb_agg(jsonb_build_object('event_id',event_id,'recorded_at',recorded_at::text,'payload_digest',encode(payload_digest,'hex'),'event',event) ORDER BY event_id COLLATE "C"),'[]'::jsonb) AS records FROM sized WHERE size<=4194304`,
          [this.productId, this.environment, after, limit],
        );
        if (
          page.rows.length !== 1 ||
          !Number.isInteger(page.rows[0]!.total) ||
          page.rows[0]!.total < 0 ||
          page.rows[0]!.total > limit ||
          !Array.isArray(page.rows[0]!.records) ||
          page.rows[0]!.records.length > page.rows[0]!.total ||
          (page.rows[0]!.total > 0 && page.rows[0]!.records.length === 0)
        )
          throw new HeimdallError("audit_unavailable");
        const selected = { rows: page.rows[0]!.records };
        for (const row of selected.rows) {
          if (
            !token(row.event_id, 128) ||
            (after !== null && row.event_id <= after) ||
            !/^[a-f0-9]{64}(?![\s\S])/.test(row.payload_digest)
          )
            throw new HeimdallError("audit_unavailable");
          const event = this.decodeStored(
            row.event,
            row.event_id,
            integer(row.recorded_at),
            row.payload_digest,
          );
          if (phase === "requests" && event.eventType === "heimdall.authorization.requested") {
            const b = bindAuditDecision(this.productId, this.environment, [event]);
            await client.query(
              "INSERT INTO heimdall.audit_decisions(product_id,environment,decision_id,request_event_id,requested_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING",
              [this.productId, this.environment, b.decisionId, b.requestEventId, b.requestedAt],
            );
            const existing = await client.query(
              "SELECT request_event_id,requested_at::text,resolution FROM heimdall.audit_decisions WHERE product_id=$1 AND environment=$2 AND decision_id=$3",
              [this.productId, this.environment, b.decisionId],
            );
            if (
              existing.rows.length !== 1 ||
              existing.rows[0].request_event_id !== b.requestEventId ||
              existing.rows[0].requested_at !== String(b.requestedAt) ||
              existing.rows[0].resolution !== "pending"
            )
              throw new HeimdallError("audit_unavailable");
          }
          if (phase === "outcomes" && event.eventType === "heimdall.authorization.completed") {
            const existing = await client.query(
              "SELECT d.request_event_id,d.requested_at::text,d.resolution,d.terminal_event_id,d.resolved_at::text,e.recorded_at::text,e.payload_digest,e.event FROM heimdall.audit_decisions d JOIN heimdall.audit_events e ON e.product_id=d.product_id AND e.environment=d.environment AND e.event_id=d.request_event_id WHERE d.product_id=$1 AND d.environment=$2 AND d.decision_id=$3 FOR UPDATE OF d",
              [this.productId, this.environment, event.decisionId],
            );
            if (existing.rows.length !== 1) throw new HeimdallError("audit_unavailable");
            const r = existing.rows[0];
            const request = this.decodeStored(
              r.event,
              r.request_event_id,
              integer(r.recorded_at),
              r.payload_digest.toString("hex"),
            );
            const b = bindAuditDecision(this.productId, this.environment, [request, event]);
            if (r.requested_at !== String(b.requestedAt))
              throw new HeimdallError("audit_unavailable");
            if (r.resolution === "pending") {
              if (r.terminal_event_id !== null || r.resolved_at !== null)
                throw new HeimdallError("audit_unavailable");
              await client.query(
                "UPDATE heimdall.audit_decisions SET resolution=$4,terminal_event_id=$5,resolved_at=$6 WHERE product_id=$1 AND environment=$2 AND decision_id=$3 AND resolution='pending'",
                [
                  this.productId,
                  this.environment,
                  b.decisionId,
                  b.resolution,
                  b.terminalEventId,
                  b.resolvedAt,
                ],
              );
            } else if (
              r.resolution !== b.resolution ||
              r.terminal_event_id !== b.terminalEventId ||
              r.resolved_at !== String(b.resolvedAt)
            )
              throw new HeimdallError("audit_unavailable");
          }
          after = row.event_id;
        }
        count += selected.rows.length;
        if (!Number.isSafeInteger(count) || (phase === "outcomes" && count > expected!))
          throw new HeimdallError("audit_unavailable");
        if (selected.rows.length === 0) {
          after = null;
          if (phase === "requests") {
            phase = "outcomes";
            expected = count;
            count = 0;
          } else {
            if (count !== expected) throw new HeimdallError("audit_unavailable");
            phase = "complete";
          }
        }
        await client.query(
          "UPDATE heimdall.audit_decision_backfill SET phase=$3,last_event_id=$4,scanned_count=$5,expected_count=$6 WHERE product_id=$1 AND environment=$2",
          [this.productId, this.environment, phase, after, count, expected],
        );
        return {
          phase,
          after,
          scannedCount: count,
          expectedCount: expected,
          processed: selected.rows.length,
        };
      },
      signal,
    );
  }
  // Internal maintenance cursor, not an authenticated public pagination token.
  async scanPendingDecisions(
    cutoff: number,
    after: {
      productId: string;
      environment: string;
      cutoff: number;
      requestedAt: number;
      decisionId: string;
    } | null,
    limit: number,
    signal?: AbortSignal,
  ) {
    const timestamp = (v: unknown): v is number =>
      typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
    if (
      !timestamp(cutoff) ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100 ||
      (after !== null &&
        (typeof after !== "object" ||
          after.productId !== this.productId ||
          after.environment !== this.environment ||
          after.cutoff !== cutoff ||
          !timestamp(after.requestedAt) ||
          after.requestedAt > cutoff ||
          !token(after.decisionId, 128)))
    )
      throw new HeimdallError("invalid_argument");
    after = after === null ? null : { ...after };
    const rows = await query<{
      primary: boolean;
      total: number;
      records: {
        decisionId: string;
        requestedAt: number;
        requestEventId: string;
        recordedAt: number;
        digest: string;
        event: Record<string, unknown>;
      }[];
    }>(
      this.pool,
      `
WITH selected AS MATERIALIZED (
 SELECT d.decision_id,d.requested_at,d.request_event_id,e.recorded_at,e.payload_digest,e.event
 FROM heimdall.audit_decisions d LEFT JOIN heimdall.audit_events e ON e.product_id=d.product_id AND e.environment=d.environment AND e.event_id=d.request_event_id
 WHERE d.product_id=$1 AND d.environment=$2 AND d.resolution='pending' AND d.requested_at<=$3
 AND ($4::bigint IS NULL OR (d.requested_at,d.decision_id COLLATE "C")>($4,$5 COLLATE "C"))
 ORDER BY d.requested_at,d.decision_id COLLATE "C" LIMIT $6
), encoded AS MATERIALIZED (
 SELECT decision_id,requested_at,jsonb_build_object('decisionId',decision_id,'requestedAt',requested_at,'requestEventId',request_event_id,'recordedAt',recorded_at,'digest',encode(payload_digest,'hex'),'event',event) AS record FROM selected
), sized AS (
 SELECT *,sum(octet_length(record::text)) OVER(ORDER BY requested_at,decision_id COLLATE "C") AS size FROM encoded
) SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
 (SELECT count(*)::int FROM selected) AS total,COALESCE(jsonb_agg(record ORDER BY requested_at,decision_id COLLATE "C"),'[]'::jsonb) AS records FROM sized WHERE size<=4194304`,
      [
        this.productId,
        this.environment,
        cutoff,
        after?.requestedAt ?? null,
        after?.decisionId ?? null,
        limit + 1,
      ],
      false,
      signal,
    );
    if (rows.length !== 1 || rows[0]!.primary !== true) throw new HeimdallError("unavailable");
    const page = rows[0]!;
    if (
      !Number.isInteger(page.total) ||
      page.total < 0 ||
      page.total > limit + 1 ||
      !Array.isArray(page.records) ||
      page.records.length > page.total ||
      (page.total > 0 && page.records.length === 0)
    )
      throw new HeimdallError("audit_unavailable");
    let previous = after;
    const items = page.records.map((row) => {
      if (
        !token(row.decisionId, 128) ||
        !timestamp(row.requestedAt) ||
        row.requestedAt > cutoff ||
        !token(row.requestEventId, 128) ||
        !timestamp(row.recordedAt) ||
        typeof row.digest !== "string" ||
        !/^[a-f0-9]{64}(?![\s\S])/.test(row.digest) ||
        (previous !== null &&
          (row.requestedAt < previous.requestedAt ||
            (row.requestedAt === previous.requestedAt && row.decisionId <= previous.decisionId)))
      )
        throw new HeimdallError("audit_unavailable");
      const request = this.decodeStored(row.event, row.requestEventId, row.recordedAt, row.digest);
      const b = bindAuditDecision(this.productId, this.environment, [request]);
      if (b.decisionId !== row.decisionId || b.requestedAt !== row.requestedAt)
        throw new HeimdallError("audit_unavailable");
      previous = {
        productId: this.productId,
        environment: this.environment,
        cutoff,
        requestedAt: row.requestedAt,
        decisionId: row.decisionId,
      };
      return { decisionId: row.decisionId, requestedAt: row.requestedAt, request };
    });
    const more = page.total > limit || page.total > items.length;
    const selected = items.slice(0, limit);
    const last = selected.at(-1);
    return {
      items: selected,
      next:
        more && last
          ? {
              productId: this.productId,
              environment: this.environment,
              cutoff,
              requestedAt: last.requestedAt,
              decisionId: last.decisionId,
            }
          : null,
    };
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
    return this.appendWith(event, async () => {}, signal);
  }
  // SDK-internal local writes only: no remote I/O, COMMIT, or result publication
  // inside mutate. Event idempotency does not deduplicate the mutation callback.
  async appendWith(
    event: Record<string, unknown>,
    mutate: (client: PoolClient) => Promise<void>,
    signal?: AbortSignal,
  ) {
    return this.appendFinalized(event, mutate, async () => {}, signal);
  }
  private async appendFinalized(
    event: Record<string, unknown>,
    mutate: (client: PoolClient) => Promise<void>,
    finalize: (client: PoolClient, event: Record<string, unknown>) => Promise<void>,
    signal?: AbortSignal,
  ) {
    if (typeof mutate !== "function") throw new HeimdallError("invalid_argument");
    const prepared = this.prepare(event);
    return transaction(
      this.pool,
      async (client) => {
        await mutate(client);
        const { snapshot, eventId, recordedAt, originalDigest } = prepared;
        await this.writePrepared(client, prepared);
        await finalize(client, snapshot);
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
  private prepare(event: Record<string, unknown>) {
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
    return { snapshot, eventId, recordedAt, originalDigest, storedEnvelope };
  }
  private async writePrepared(
    client: PoolClient,
    prepared: ReturnType<PostgresAudit["prepare"]>,
    existingOnly = false,
  ) {
    const { eventId, recordedAt, originalDigest, storedEnvelope } = prepared;
    const parameters = [this.productId, this.environment, eventId];
    const inserted = existingOnly
      ? { rowCount: 0 }
      : await client.query(
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
    if (row.payload_digest.toString("hex") !== originalDigest) throw new HeimdallError("conflict");
    if (row.recorded_at !== String(recordedAt)) throw new HeimdallError("audit_unavailable");
    this.decodeStored(row.event, eventId, recordedAt, originalDigest);
    if (inserted.rowCount === 1) {
      for (const sink of this.sinks)
        await client.query(
          "INSERT INTO heimdall.audit_deliveries(product_id,environment,event_id,sink_id) VALUES($1,$2,$3,$4)",
          [...parameters, sink],
        );
    }
  }
  // Prepared v2 port: factory/readiness activation is a separate deployment gate.
  async appendDecision(event: Record<string, unknown>, signal?: AbortSignal) {
    if (
      !["heimdall.authorization.requested", "heimdall.authorization.completed"].includes(
        String(event.eventType),
      ) ||
      event.outcome === "outcome_unknown"
    )
      throw new HeimdallError("invalid_argument");
    return this.appendFinalized(
      event,
      async () => {},
      (client, snapshot) => this.persistDecision(client, snapshot).then(() => {}),
      signal,
    );
  }
  // Actor authentication and maintenance authorization belong to the caller.
  async recoverDecision(
    terminal: Record<string, unknown>,
    administration: Record<string, unknown>,
    cutoff: number,
    signal?: AbortSignal,
  ) {
    const completion = this.prepare(terminal),
      recovery = this.prepare(administration);
    validateRecoveryPair(completion.snapshot, recovery.snapshot, cutoff);
    const receipt = await this.appendFinalized(
      completion.snapshot,
      async () => {},
      async (client, snapshot) => {
        const changed = await this.persistDecision(client, snapshot, cutoff);
        await this.writePrepared(client, recovery, !changed);
      },
      signal,
    );
    return {
      terminal: receipt,
      administration: {
        productId: this.productId,
        environment: this.environment,
        eventId: recovery.eventId,
        recordedAt: recovery.recordedAt,
        payloadDigest: recovery.originalDigest,
      },
    };
  }
  private async persistDecision(
    client: PoolClient,
    snapshot: Record<string, unknown>,
    cutoff?: number,
  ) {
    const scope = [this.productId, this.environment, snapshot.decisionId];
    if (snapshot.eventType === "heimdall.authorization.requested") {
      const b = bindAuditDecision(this.productId, this.environment, [snapshot]);
      await client.query(
        "INSERT INTO heimdall.audit_decisions(product_id,environment,decision_id,request_event_id,requested_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING",
        [...scope, b.requestEventId, b.requestedAt],
      );
      const rows = await client.query(
        "SELECT request_event_id,requested_at::text FROM heimdall.audit_decisions WHERE product_id=$1 AND environment=$2 AND decision_id=$3 FOR UPDATE",
        scope,
      );
      if (
        rows.rows.length !== 1 ||
        rows.rows[0].request_event_id !== b.requestEventId ||
        rows.rows[0].requested_at !== String(b.requestedAt)
      )
        throw new HeimdallError("conflict");
      return false;
    }
    const rows = await client.query(
      "SELECT d.request_event_id,d.requested_at::text,d.resolution,d.terminal_event_id,d.resolved_at::text,e.recorded_at::text,e.payload_digest,e.event FROM heimdall.audit_decisions d JOIN heimdall.audit_events e ON e.product_id=d.product_id AND e.environment=d.environment AND e.event_id=d.request_event_id WHERE d.product_id=$1 AND d.environment=$2 AND d.decision_id=$3 FOR UPDATE OF d",
      scope,
    );
    if (rows.rows.length !== 1) throw new HeimdallError("audit_unavailable");
    const r = rows.rows[0];
    const request = this.decodeStored(
      r.event,
      r.request_event_id,
      Number(r.recorded_at),
      r.payload_digest.toString("hex"),
    );
    const b = bindAuditDecision(this.productId, this.environment, [request, snapshot]);
    if (cutoff !== undefined && b.requestedAt > cutoff) throw new HeimdallError("conflict");
    if (r.requested_at !== String(b.requestedAt)) throw new HeimdallError("audit_unavailable");
    if (r.resolution !== "pending") {
      if (
        r.resolution !== b.resolution ||
        r.terminal_event_id !== b.terminalEventId ||
        r.resolved_at !== String(b.resolvedAt)
      )
        throw new HeimdallError("conflict");
      return false;
    }
    if (r.terminal_event_id !== null || r.resolved_at !== null)
      throw new HeimdallError("audit_unavailable");
    const updated = await client.query(
      "UPDATE heimdall.audit_decisions SET resolution=$4,terminal_event_id=$5,resolved_at=$6 WHERE product_id=$1 AND environment=$2 AND decision_id=$3 AND resolution='pending'",
      [...scope, b.resolution, b.terminalEventId, b.resolvedAt],
    );
    if (updated.rowCount !== 1) throw new HeimdallError("audit_unavailable");
    return true;
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
