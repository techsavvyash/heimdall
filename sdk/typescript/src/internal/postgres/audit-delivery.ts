import type { Pool } from "pg";
import { randomInt } from "node:crypto";
import { validateEndpoint } from "../endpoints.js";
import { auditEventValidator, type CustomAuditValidator } from "./custom-audit.js";
import { checkCancellation, HeimdallError } from "../errors.js";
import { PostgresAudit } from "./audit.js";
import { readSnapshot } from "./query.js";
import { retentionSQL, retentionArgs } from "./readiness.js";

export function createAuditDelivery(
  pool: Pool,
  options: {
    productId: string;
    environment: string;
    sinkId: string;
    tenantId?: string;
    endpoint: string;
    token: string;
    transportProfile?: "production" | "loopback";
    validateCustom?: CustomAuditValidator;
  },
) {
  const { productId, environment, sinkId, tenantId, endpoint, token, validateCustom } = options;
  if (
    (tenantId !== undefined &&
      (typeof tenantId !== "string" || !/^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(tenantId))) ||
    typeof endpoint !== "string" ||
    typeof token !== "string" ||
    !/^[\x21-\x7e]{1,4096}(?![\s\S])/.test(token)
  )
    throw new HeimdallError("invalid_argument");
  validateEndpoint(endpoint, options.transportProfile ?? "production");
  const audit = PostgresAudit.forRetainedRuntime(
    pool,
    productId,
    environment,
    [sinkId],
    auditEventValidator(validateCustom),
  );
  const scope = { productId, environment, sinkId };
  return Object.freeze({
    /** Read sink backlog and database-wide audit capacity without decoding event payloads. */
    metrics(signal: AbortSignal) {
      if (!(signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
      const now = Date.now();
      return readSnapshot(
        pool,
        async (client) => {
          const ready = await client.query<{ ready: boolean }>(
            `SELECT ready AND current_setting('default_transaction_read_only')='off' AS ready
             FROM (${retentionSQL}) admission`,
            retentionArgs,
          );
          if (ready.rows[0]?.ready !== true) throw new HeimdallError("unavailable");
          const {
            rows: [backlog],
          } = await client.query<{
            pending: string;
            due: string;
            quarantined: string;
            oldest: string | null;
          }>(
            `SELECT count(*) FILTER (WHERE d.state='pending')::text AS pending,
            count(*) FILTER (WHERE d.state='pending' AND d.next_attempt_at<=$4)::text AS due,
            count(*) FILTER (WHERE d.state='quarantined')::text AS quarantined,
            min(e.recorded_at) FILTER (WHERE d.state='pending')::text AS oldest
          FROM heimdall.audit_deliveries d JOIN heimdall.audit_events e USING(product_id,environment,event_id)
          WHERE d.product_id=$1 AND d.environment=$2 AND d.sink_id=$3
            ${tenantId === undefined ? "" : "AND EXISTS (SELECT 1 FROM heimdall_retention.event_identities i WHERE i.product_id=d.product_id AND i.environment=d.environment AND i.event_id=d.event_id AND i.tenant_id=$5)"}
            AND d.state IN ('pending','quarantined')`,
            tenantId === undefined
              ? [productId, environment, sinkId, now]
              : [productId, environment, sinkId, now, tenantId],
          );
          const {
            rows: [capacity],
          } = await client.query<{
            events: string;
            eventLimit: string;
            bytes: string;
            byteLimit: string;
            deliveries: string;
            deliveryLimit: string;
            retainedRows: string;
            retainedRowLimit: string;
          }>(`SELECT (used_events+reserved_events)::text AS events,event_limit::text AS "eventLimit",
          (used_bytes+reserved_bytes)::text AS bytes,p.byte_limit::text AS "byteLimit",
          (used_deliveries+reserved_deliveries)::text AS deliveries,delivery_limit::text AS "deliveryLimit",
          (r.used_rows+r.reserved_rows)::text AS "retainedRows",
          LEAST(r.row_limit,r.byte_limit/8192)::text AS "retainedRowLimit"
          FROM heimdall_capacity.policy p CROSS JOIN heimdall_retention.capacity r
          WHERE p.singleton AND r.singleton`);
          if (!backlog || !capacity) throw new HeimdallError("unavailable");
          return {
            pending: Number(backlog.pending),
            due: Number(backlog.due),
            quarantined: Number(backlog.quarantined),
            oldestPendingAgeMs:
              backlog.oldest === null ? null : Math.max(0, now - Number(backlog.oldest)),
            capacity: {
              events: Number(capacity.events),
              eventLimit: Number(capacity.eventLimit),
              bytes: Number(capacity.bytes),
              byteLimit: Number(capacity.byteLimit),
              deliveries: Number(capacity.deliveries),
              deliveryLimit: Number(capacity.deliveryLimit),
              retainedRows: Number(capacity.retainedRows),
              retainedRowLimit: Number(capacity.retainedRowLimit),
            },
          };
        },
        signal,
      );
    },
    async deliver(limit: number, signal: AbortSignal) {
      if (!(signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
      const operation = AbortSignal.any([signal, AbortSignal.timeout(30000)]);
      const pending = await audit.pending(sinkId, Date.now(), limit, operation, tenantId);
      const result = { attempted: 0, delivered: 0, quarantined: 0 };
      const claimed: { eventId: string; event: Record<string, unknown>; attempt: number }[] = [];
      for (const item of pending) {
        if (item.event === null || item.attempts === 2147483647) {
          if (await audit.quarantine(sinkId, item.eventId, item.attempts, operation))
            result.quarantined++;
          continue;
        }
        const now = Date.now();
        const retry = Math.min(300000, 60000 * 2 ** Math.min(item.attempts, 3));
        const attempt = await audit.beginAttempt(
          sinkId,
          item.eventId,
          item.attempts,
          now,
          now + retry + randomInt(5000),
          operation,
        );
        if (attempt !== null) claimed.push({ eventId: item.eventId, event: item.event, attempt });
      }
      result.attempted = claimed.length;
      async function send(items: typeof claimed): Promise<void> {
        const batch = { ...scope, eventIds: items.map((item) => item.eventId) };
        const request = AbortSignal.any([operation, AbortSignal.timeout(5000)]);
        let response: Response;
        try {
          response = await fetch(endpoint, {
            method: "POST",
            redirect: "error",
            signal: request,
            headers: { authorization: "Bearer " + token, "content-type": "application/json" },
            body: JSON.stringify({ ...batch, events: items.map((item) => item.event) }),
          });
        } catch {
          checkCancellation(request);
          throw new HeimdallError("unavailable");
        }
        if (response.status === 422) {
          await response.body?.cancel();
          if (items.length === 1) {
            const item = items[0]!;
            if (await audit.quarantine(sinkId, item.eventId, item.attempt, operation))
              result.quarantined++;
          } else {
            const middle = Math.floor(items.length / 2);
            await send(items.slice(0, middle));
            await send(items.slice(middle));
          }
          return;
        }
        if (!response.ok || !response.body) {
          await response.body?.cancel();
          throw new HeimdallError("unavailable");
        }
        const reader = response.body.getReader();
        const chunks: Uint8Array[] = [];
        let bytes = 0;
        let receipt: Parameters<typeof audit.acknowledge>[1];
        try {
          for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            bytes += value.length;
            if (bytes > 32768) throw new Error();
            chunks.push(value);
          }
          receipt = JSON.parse(Buffer.concat(chunks, bytes).toString("utf8"));
          if (!receipt || typeof receipt !== "object") throw new Error();
        } catch {
          checkCancellation(request);
          throw new HeimdallError("unavailable");
        } finally {
          await reader.cancel();
        }
        await audit.acknowledge(batch, receipt, Date.now(), operation);
        result.delivered += receipt.eventIds.length;
      }
      if (claimed.length) await send(claimed);
      return result;
    },
    requeue(eventId: string, attempts: number, signal: AbortSignal) {
      if (!(signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
      return audit.requeueQuarantined(sinkId, eventId, attempts, Date.now(), signal, tenantId);
    },
  });
}
