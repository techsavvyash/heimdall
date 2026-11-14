import { createHash } from "node:crypto";
import type { Pool } from "pg";
import {
  createIdentityEventReader,
  type IdentityEventReaderOptions,
} from "../../provisioning/identity-events.js";
import { providerId } from "../../provisioning/provider-request.js";
import { checkCancellation, HeimdallError } from "../errors.js";
import { PostgresAudit } from "./audit.js";
import { auditEventValidator } from "./custom-audit.js";
import { query, transaction } from "./query.js";

export type IdentityCollectorOptions = IdentityEventReaderOptions & {
  productId: string;
  environment: string;
  tenantId: string;
  auditSinks: readonly string[];
  startMicros: bigint;
  // Exact aggregate IDs explicitly assigned to this tenant by the operator.
  aggregates: readonly { type: string; id: string }[];
};

export type IdentityCollectorCheckpoint = {
  nextMicros: bigint;
  mappingDigest: string;
  mappingRevision: number;
};

const maximumTime = 253402300799999999n;
function instant(micros: bigint) {
  return new Date(Number(micros / 1000n))
    .toISOString()
    .replace("Z", String(micros % 1000n).padStart(3, "0") + "Z");
}
const predicate =
  "product_id=$1 AND environment=$2 AND issuer=$3 AND organization_id=$4 AND tenant_id=$5";

/** Collect certified historical windows for an explicitly mapped product tenant. */
export function createIdentityCollector(pool: Pool, options: IdentityCollectorOptions) {
  const { productId, environment, organizationId, tenantId, startMicros, auditSinks, aggregates } =
    options;
  const reader = createIdentityEventReader(options);
  const issuer = options.issuer.replace(/\/$/, "");
  if (
    !providerId(tenantId) ||
    typeof startMicros !== "bigint" ||
    startMicros < 1n ||
    startMicros >= maximumTime ||
    typeof pool?.connect !== "function" ||
    !Array.isArray(auditSinks) ||
    !auditSinks.length ||
    !Array.isArray(aggregates) ||
    !aggregates.length ||
    aggregates.length > 10000 ||
    aggregates.some(
      (a) =>
        !a ||
        typeof a.type !== "string" ||
        !/^[A-Za-z0-9_.-]{1,128}(?![\s\S])/.test(a.type) ||
        !providerId(a.id),
    )
  )
    throw new HeimdallError("invalid_argument");
  const allowed = new Set(aggregates.map((a) => a.type + ":" + a.id));
  const mapping = createHash("sha256")
    .update(JSON.stringify([...allowed].sort()))
    .digest("hex");
  const audit = PostgresAudit.forRetainedRuntime(
    pool,
    productId,
    environment,
    auditSinks,
    auditEventValidator(),
  );
  const scope = [productId, environment, issuer, organizationId, tenantId];
  const checkpointSQL =
    "SELECT next_micros::text AS next,mapping_digest,mapping_revision::text AS revision FROM heimdall_directory.event_checkpoints WHERE " +
    predicate;
  type Row = { next: string; mapping_digest: string; revision: string };
  const decode = (row: Row): IdentityCollectorCheckpoint => ({
    nextMicros: BigInt(row.next),
    mappingDigest: row.mapping_digest,
    mappingRevision: Number(row.revision),
  });
  async function checkpoint(signal: AbortSignal) {
    if (!(signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
    const [row] = await query<Row>(pool, checkpointSQL, scope, false, signal);
    return row ? decode(row) : null;
  }
  return Object.freeze({
    checkpoint,
    /** Activate this mapping at the existing checkpoint. Call from trusted operator code. */
    async activateMapping(expected: IdentityCollectorCheckpoint, signal: AbortSignal) {
      if (
        !(signal instanceof AbortSignal) ||
        typeof expected?.nextMicros !== "bigint" ||
        expected.nextMicros < 1n ||
        expected.nextMicros > maximumTime ||
        !Number.isSafeInteger(expected.mappingRevision) ||
        expected.mappingRevision < 0 ||
        expected.mappingRevision >= Number.MAX_SAFE_INTEGER ||
        typeof expected.mappingDigest !== "string" ||
        !/^[a-f0-9]{64}(?![\s\S])/.test(expected.mappingDigest)
      )
        throw new HeimdallError("invalid_argument");
      const { nextMicros, mappingDigest, mappingRevision } = expected;
      return transaction(
        pool,
        async (client) => {
          const {
            rows: [row],
          } = await client.query<Row>(checkpointSQL + " FOR UPDATE", scope);
          if (!row) throw new HeimdallError("not_found");
          const current = decode(row);
          const targetRevision = mappingRevision + Number(mapping !== mappingDigest);
          if (
            current.nextMicros === nextMicros &&
            current.mappingDigest === mapping &&
            current.mappingRevision === targetRevision
          )
            return current;
          if (
            current.nextMicros !== nextMicros ||
            current.mappingDigest !== mappingDigest ||
            current.mappingRevision !== mappingRevision
          )
            throw new HeimdallError("conflict");
          await client.query(
            "UPDATE heimdall_directory.event_checkpoints SET mapping_digest=$6,mapping_revision=$7 WHERE " +
              predicate,
            [...scope, mapping, targetRevision],
          );
          return { nextMicros, mappingDigest: mapping, mappingRevision: targetRevision };
        },
        signal,
      );
    },
    async collect(
      coverage: { retainedFromMicros: bigint; visibleBeforeMicros: bigint; validUntilMs: number },
      signal: AbortSignal,
    ) {
      if (
        !(signal instanceof AbortSignal) ||
        typeof coverage?.retainedFromMicros !== "bigint" ||
        typeof coverage.visibleBeforeMicros !== "bigint" ||
        !Number.isSafeInteger(coverage.validUntilMs) ||
        coverage.retainedFromMicros < 0n ||
        coverage.visibleBeforeMicros <= coverage.retainedFromMicros ||
        coverage.visibleBeforeMicros > BigInt(Date.now()) * 1000n ||
        coverage.visibleBeforeMicros > maximumTime
      )
        throw new HeimdallError("invalid_argument");
      checkCancellation(signal);
      const { retainedFromMicros, visibleBeforeMicros, validUntilMs } = coverage;
      if (validUntilMs <= Date.now()) throw new HeimdallError("unavailable");
      const current = await checkpoint(signal);
      if (current && current.mappingDigest !== mapping) throw new HeimdallError("conflict");
      const next = current?.nextMicros ?? startMicros;
      const revision = current?.mappingRevision ?? 0;
      if (next <= retainedFromMicros) throw new HeimdallError("unavailable");
      if (next >= visibleBeforeMicros) return { nextMicros: next, recorded: 0 };
      let upper = next + 60000000n < visibleBeforeMicros ? next + 60000000n : visibleBeforeMicros;
      const operation = AbortSignal.any([signal, AbortSignal.timeout(30000)]);
      let selected;
      for (;;) {
        if (validUntilMs <= Date.now()) throw new HeimdallError("unavailable");
        const scan = await reader.readWindow(instant(next - 1n), instant(upper), operation);
        selected = scan.events.filter((event) =>
          allowed.has(event.aggregateType + ":" + event.aggregateId),
        );
        if (selected.length <= 256) break;
        if (upper - next === 1n) throw new HeimdallError("unavailable");
        upper = next + (upper - next) / 2n;
      }
      if (validUntilMs <= Date.now()) throw new HeimdallError("unavailable");
      const recordedAt = Date.now();
      const events = selected.map((event) => {
        const id = createHash("sha256")
          .update(
            JSON.stringify([
              issuer,
              organizationId,
              tenantId,
              event.aggregateType,
              event.aggregateId,
              event.sequence,
            ]),
          )
          .digest("hex");
        return {
          schemaVersion: 1,
          eventId: id,
          eventType: "heimdall.identity.observed",
          eventVersion: 1,
          occurredAt: Date.parse(event.creationDate),
          recordedAt,
          productId,
          environment,
          tenantId,
          actor: { kind: "unknown" },
          target: { type: "zitadel_aggregate", id: event.aggregateId },
          action: event.type,
          outcome: "succeeded",
          requestId: id,
          source: "zitadel",
          sdkVersion: "0.0.0",
          attributes: {
            issuer,
            organizationId,
            aggregateType: event.aggregateType,
            sequence: event.sequence,
            creationDate: event.creationDate,
          },
        };
      });
      await audit.appendIdentityWindow(
        events,
        async (client) => {
          if (validUntilMs <= Date.now()) throw new HeimdallError("unavailable");
          await client.query(
            "INSERT INTO heimdall_directory.event_checkpoints(product_id,environment,issuer,organization_id,tenant_id,next_micros,mapping_digest) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING",
            [...scope, startMicros.toString(), mapping],
          );
          const changed = await client.query(
            "UPDATE heimdall_directory.event_checkpoints SET next_micros=$7 WHERE " +
              predicate +
              " AND next_micros=$6 AND mapping_digest=$8 AND mapping_revision=$9",
            [...scope, next.toString(), upper.toString(), mapping, revision],
          );
          if (changed.rowCount !== 1) throw new HeimdallError("conflict");
        },
        operation,
      );
      return { nextMicros: upper, recorded: events.length };
    },
  });
}
