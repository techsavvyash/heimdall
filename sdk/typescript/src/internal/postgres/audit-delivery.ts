import type { Pool } from "pg";
import { randomInt } from "node:crypto";
import { validateEndpoint } from "../endpoints.js";
import { auditEventValidator, type CustomAuditValidator } from "./custom-audit.js";
import { checkCancellation, HeimdallError } from "../errors.js";
import { PostgresAudit } from "./audit.js";

export function createAuditDelivery(
  pool: Pool,
  options: {
    productId: string;
    environment: string;
    sinkId: string;
    endpoint: string;
    token: string;
    transportProfile?: "production" | "loopback";
    validateCustom?: CustomAuditValidator;
  },
) {
  const { productId, environment, sinkId, endpoint, token, validateCustom } = options;
  if (
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
    async deliver(limit: number, signal: AbortSignal) {
      if (!(signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
      const operation = AbortSignal.any([signal, AbortSignal.timeout(30000)]);
      const pending = await audit.pending(sinkId, Date.now(), limit, operation);
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
      return audit.requeueQuarantined(sinkId, eventId, attempts, Date.now(), signal);
    },
  });
}
