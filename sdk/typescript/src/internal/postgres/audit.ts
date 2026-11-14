import { validateAuditRetirement, type AuditRetirementRequest } from "../audit-retire.js";
import { validateAuditHold, type AuditHoldRequest } from "../audit-hold.js";
import { classifyAuditRetention, defaultAuditRetentionMs } from "../audit-retention.js";
import { buildRetentionIdentities } from "../retention-identities.js";
import {
  sessionChecksum,
  decisionChecksum,
  capacityChecksum,
  capacityPreparationChecksum,
  capacitySQL,
  retentionSQL,
  retentionChecksums,
  retentionArgs,
  lockRetentionAdmission,
} from "./readiness.js";
import {
  capacityWrite,
  lockAuditCapacity,
  lockRuntimeCapacity,
  chargeAuditEvent,
  reserveAuditDecision,
  resolveAuditCapacity,
  checkAuditReservation,
} from "./audit-capacity.js";
import { validateRecoveryPair, recoveryEventId } from "../audit-recovery.js";
import { bindAuditDecision } from "../audit-decision-binding.js";
import type { Pool, PoolClient } from "pg";
import { auditDigest, validateDeliveryReceipt } from "../audit-receipts.js";
import { HeimdallError } from "../errors.js";
import { transaction, query, readSnapshot } from "./query.js";
const token = (v: unknown, max: number): v is string =>
  typeof v === "string" && v.length > 0 && v.length <= max && !/[^A-Za-z0-9_-]/.test(v);
export type RetirementCursor = {
  productId: string;
  environment: string;
  tenantId: string | null;
  now: number;
  retainForMs: number;
  eventId: string;
};
type RetentionEligibilityRow = {
  root: RetainedRow;
  bound: boolean;
  resolution: string;
  held: boolean;
  retry: boolean;
  members: {
    eventId: string;
    row: RetainedRow;
    deliveries: { sinkId: string; state: string; deliveredAt: number | null }[];
  }[];
};
type RetainedRow = {
  ready: boolean;
  present: boolean;
  live: boolean;
  markers: number;
  tenant_id: string | null;
  identity: ReturnType<typeof buildRetentionIdentities>[number];
  payload: Record<string, unknown>;
  valid: boolean;
};
// Internal persistence port. Factory must supply schema + semantic validation;
// actor verification and event origin remain the authenticated SDK layer's job.
export class PostgresAudit {
  private retainedRuntime = false;
  static forRetainedRuntime(
    pool: Pool,
    productId: string,
    environment: string,
    sinks: readonly string[],
    validate: (event: Record<string, unknown>) => void,
  ) {
    const audit = new PostgresAudit(pool, productId, environment, sinks, validate);
    audit.retainedRuntime = true;
    return Object.freeze({
      append: audit.appendRetained.bind(audit),
      pending: audit.pending.bind(audit),
      beginAttempt: audit.beginAttempt.bind(audit),
      deferAttempt: audit.deferAttempt.bind(audit),
      quarantine: audit.quarantine.bind(audit),
      requeueQuarantined: audit.requeueQuarantined.bind(audit),
      acknowledge: audit.acknowledge.bind(audit),
      retire: audit.retire.bind(audit),
      applyHold: audit.applyHold.bind(audit),
      appendWith: audit.appendRetainedWith.bind(audit),
      appendIdentityWindow: audit.appendIdentityWindow.bind(audit),
      recover: audit.recoverRetained.bind(audit),
      recoverRuntime: audit.recoverRetained.bind(audit),
      scanPendingDecisions: audit.scanPendingDecisions.bind(audit),
      inspect: audit.inspectRetainedEvent.bind(audit),
      readResolution: audit.readRetainedResolution.bind(audit),
      resolveReceipt: audit.resolveRetainedReceipt.bind(audit),
      eligibility: audit.inspectRetentionEligibility.bind(audit),
      scanRetirementCandidates: audit.scanRetirementCandidates.bind(audit),
    });
  }
  private retainedHistorySQL(sql: string) {
    return this.retainedRuntime
      ? sql.replaceAll(
          capacitySQL,
          retentionSQL.replace(/\$[56]/g, (x) => (x === "$5" ? "$8" : "$9")),
        )
      : sql;
  }
  private retainedHistoryArgs(id: string) {
    const args: unknown[] = [
      sessionChecksum,
      decisionChecksum,
      capacityChecksum,
      capacityPreparationChecksum,
      this.productId,
      this.environment,
      id,
    ];
    if (this.retainedRuntime) args.push(retentionChecksums, retentionChecksums[0]);
    return args;
  }

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
  // Prepared primary inspection only; never a fresh authorization result.
  async inspectRetainedEvent(tenantId: string | null, eventId: string, signal?: AbortSignal) {
    return this.inspectRetainedHistory(tenantId, eventId, true, signal);
  }
  // Receipt resolution is read-only. It cannot authorize re-executing old work.
  async resolveRetainedReceipt(event: Record<string, unknown>, signal?: AbortSignal) {
    const p = this.prepare(event);
    const tenant = (p.snapshot.tenantId ?? null) as string | null;
    const result = await this.inspectRetainedHistory(tenant, p.eventId, false, signal);
    if (!result) return null;
    if (result.identity.digest !== p.originalDigest || result.identity.recordedAt !== p.recordedAt)
      throw new HeimdallError("conflict");
    return {
      productId: this.productId,
      environment: this.environment,
      eventId: p.eventId,
      recordedAt: result.identity.recordedAt,
      payloadDigest: result.identity.digest,
    };
  }
  private async inspectRetainedHistory(
    tenantId: string | null,
    eventId: string,
    filterTenant: boolean,
    signal?: AbortSignal,
  ) {
    if ((tenantId !== null && !token(tenantId, 128)) || !token(eventId, 128))
      throw new HeimdallError("invalid_argument");
    const rows = await query<RetainedRow>(
      this.pool,
      this.retainedHistorySQL(retainedReadSQL),
      this.retainedHistoryArgs(eventId),
      false,
      signal,
    );
    if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
    return this.decodeRetainedRow(rows[0]!, tenantId, eventId, filterTenant);
  }
  private decodeRetainedRow(
    row: RetainedRow,
    tenantId: string | null,
    eventId: string,
    filterTenant: boolean,
  ) {
    if (!row.ready) throw new HeimdallError("unavailable");
    if (!row.present) {
      if (row.live || row.markers !== 0) throw new HeimdallError("audit_unavailable");
      return null;
    }
    if (filterTenant && row.tenant_id !== tenantId) return null;
    if (!row.valid || row.markers !== (row.live ? 0 : 1))
      throw new HeimdallError("audit_unavailable");
    const identity = row.identity as ReturnType<typeof buildRetentionIdentities>[number];
    if (!row.live) return { state: "retired" as const, identity, event: null };
    const event = this.decodeStored(row.payload, eventId, identity.recordedAt, identity.digest);
    const kind =
      event.eventType === "heimdall.authorization.requested"
        ? "request"
        : event.eventType === "heimdall.authorization.completed"
          ? "terminal"
          : event.eventType === "heimdall.recovery.completed" &&
              event.action === "authorization.recover"
            ? "recovery"
            : "ordinary";
    if (
      (event.tenantId ?? null) !== row.tenant_id ||
      kind !== identity.kind ||
      (kind !== "ordinary" && event.decisionId !== identity.decisionId)
    )
      throw new HeimdallError("audit_unavailable");
    return { state: "live" as const, identity, event };
  }
  // One primary snapshot; eligibility is advisory and grants no deletion authority.
  async inspectRetentionEligibility(
    tenantId: string | null,
    eventId: string,
    now: number,
    retainForMs = defaultAuditRetentionMs,
    signal?: AbortSignal,
  ) {
    if (
      (tenantId !== null && !token(tenantId, 128)) ||
      !token(eventId, 128) ||
      !Number.isSafeInteger(now) ||
      now < 0 ||
      !Number.isSafeInteger(retainForMs) ||
      retainForMs < 1
    )
      throw new HeimdallError("invalid_argument");
    const rows = await query<RetentionEligibilityRow>(
      this.pool,
      this.retainedHistorySQL(retentionEligibilitySQL),
      this.retainedHistoryArgs(eventId),
      false,
      signal,
    );
    if (rows.length !== 1) throw new HeimdallError("unavailable");
    return (
      this.decodeRetirementCandidate(rows[0]!, tenantId, eventId, now, retainForMs)?.reason ?? null
    );
  }
  private async scanRetirementCandidates(
    tenantId: string | null,
    now: number,
    retainForMs: number,
    after: RetirementCursor | null,
    limit: number,
    signal?: AbortSignal,
  ): Promise<{ eventIds: string[]; next: RetirementCursor | null }> {
    if (
      (tenantId !== null && !token(tenantId, 128)) ||
      !Number.isSafeInteger(now) ||
      now < 0 ||
      !Number.isSafeInteger(retainForMs) ||
      retainForMs < 1 ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 8 ||
      (after !== null &&
        (!after ||
          after.productId !== this.productId ||
          after.environment !== this.environment ||
          after.tenantId !== tenantId ||
          after.now !== now ||
          after.retainForMs !== retainForMs ||
          !token(after.eventId, 128)))
    )
      throw new HeimdallError("invalid_argument");
    const last = after?.eventId ?? "";
    return readSnapshot(
      this.pool,
      async (client) => {
        const {
          rows: [page],
        } = await client.query<{
          ready: boolean;
          scanned: number;
          last_scanned: string | null;
          missing_identity: boolean;
          selected: string[];
        }>(retirementPageSQL, [
          ...retentionArgs,
          this.productId,
          this.environment,
          last,
          limit,
          tenantId,
          now - retainForMs,
        ]);
        if (!page?.ready) throw new HeimdallError("unavailable");
        if (
          page.missing_identity ||
          !Number.isInteger(page.scanned) ||
          page.scanned < 0 ||
          page.scanned > limit ||
          (page.scanned > 0 && !token(page.last_scanned, 128)) ||
          !Array.isArray(page.selected) ||
          page.selected.length > page.scanned
        )
          throw new HeimdallError("audit_unavailable");
        const eventIds: string[] = [];
        for (const id of page.selected) {
          const { rows } = await client.query<RetentionEligibilityRow>(
            this.retainedHistorySQL(retentionEligibilitySQL),
            this.retainedHistoryArgs(id),
          );
          if (rows.length !== 1) throw new HeimdallError("unavailable");
          const candidate = this.decodeRetirementCandidate(
            rows[0]!,
            tenantId,
            id,
            now,
            retainForMs,
          );
          if (!candidate) throw new HeimdallError("audit_unavailable");
          if (candidate.reason === "candidate") eventIds.push(id);
        }
        return {
          eventIds,
          next:
            page.scanned === limit
              ? {
                  productId: this.productId,
                  environment: this.environment,
                  tenantId,
                  now,
                  retainForMs,
                  eventId: page.last_scanned!,
                }
              : null,
        };
      },
      signal,
    );
  }
  private decodeRetirementCandidate(
    r: RetentionEligibilityRow,
    tenantId: string | null,
    eventId: string,
    now: number,
    retainForMs: number,
  ) {
    const root = this.decodeRetainedRow(r.root, tenantId, eventId, true);
    if (!root) return null;
    if (root.state === "retired") throw new HeimdallError("not_found");
    if (!["ordinary", "request"].includes(root.identity.kind))
      throw new HeimdallError("invalid_argument");
    if (!r.bound) throw new HeimdallError("audit_unavailable");
    const scope = { productId: this.productId, environment: this.environment, tenantId };
    const sources = [];
    const events = [];
    for (const member of r.members) {
      const result = this.decodeRetainedRow(member.row, tenantId, member.eventId, true);
      if (!result || result.state !== "live") throw new HeimdallError("audit_unavailable");
      const i = result.identity;
      sources.push({
        event: result.event,
        digest: i.digest,
        routing: i.routing,
        deliveryRows: member.deliveries.length,
      });
      events.push({
        ...scope,
        eventId: i.eventId,
        recordedAt: i.recordedAt,
        deliveries: member.deliveries,
      });
    }
    buildRetentionIdentities(scope, r.resolution, sources);
    const reason = classifyAuditRetention(
      scope,
      {
        resolution: r.resolution,
        holdState: r.held ? "held" : "clear",
        retryState: r.retry ? "protected" : "clear",
        events,
      },
      now,
      retainForMs,
    );
    return { reason, ids: events.map((e) => e.eventId), decisionId: root.identity.decisionId };
  }
  // Unknown decisions are absent; retired payloads are explicitly unavailable.
  async readRetainedResolution(decisionId: string, signal?: AbortSignal) {
    if (!token(decisionId, 128)) throw new HeimdallError("invalid_argument");
    const rows = await query<{
      root: RetainedRow;
      resolution: string | null;
      bound: boolean;
      source_present: boolean;
      members: { eventId: string; row: RetainedRow }[];
    }>(
      this.pool,
      this.retainedHistorySQL(retainedResolutionSQL),
      this.retainedHistoryArgs(decisionId),
      false,
      signal,
    );
    if (rows.length !== 1) throw new HeimdallError("unavailable");
    const r = rows[0]!;
    const root = this.decodeRetainedRow(
      r.root,
      r.root.tenant_id,
      r.root.identity?.eventId ?? "",
      true,
    );
    if (!root) {
      if (r.source_present) throw new HeimdallError("audit_unavailable");
      return null;
    }
    if (
      root.identity.kind !== "request" ||
      root.identity.decisionId !== decisionId ||
      r.resolution === null
    )
      throw new HeimdallError("audit_unavailable");
    const expected = ({ pending: 1, completed: 2, recovered: 3 } as Record<string, number>)[
      r.resolution
    ];
    if (!expected || !Array.isArray(r.members) || r.members.length !== expected)
      throw new HeimdallError("audit_unavailable");
    const sources = [];
    const seen = new Set<string>();
    let request: Record<string, unknown> | null = null;
    let terminal: Record<string, unknown> | null = null;
    let administration: Record<string, unknown> | null = null;
    for (const m of r.members) {
      const member = this.decodeRetainedRow(m.row, r.root.tenant_id, m.eventId, true);
      if (
        !member ||
        member.state !== root.state ||
        seen.has(m.eventId) ||
        member.identity.decisionId !== decisionId
      )
        throw new HeimdallError("audit_unavailable");
      seen.add(m.eventId);
      if (member.state === "live") {
        sources.push({
          event: member.event,
          digest: member.identity.digest,
          routing: member.identity.routing,
          deliveryRows: member.identity.routing.length,
        });
        switch (member.identity.kind) {
          case "request":
            request = member.event;
            break;
          case "terminal":
            terminal = member.event;
            break;
          case "recovery":
            administration = member.event;
            break;
          default:
            throw new HeimdallError("audit_unavailable");
        }
      }
    }
    if (root.state === "retired") throw new HeimdallError("not_found");
    if (!r.bound) throw new HeimdallError("audit_unavailable");
    buildRetentionIdentities(
      { productId: this.productId, environment: this.environment, tenantId: r.root.tenant_id },
      r.resolution,
      sources,
    );
    return {
      decisionId,
      state: r.resolution as "pending" | "completed" | "recovered",
      request: request!,
      terminal,
      administration,
    };
  }
  // Prepared deployment port only: callers drain writers and install the pinned
  // identity schema. This does not activate retention or provide a runtime API.
  async retainIdentityGroup(
    tenantId: string | null,
    resolution: string,
    eventIds: string[],
    signal?: AbortSignal,
  ) {
    if (
      (tenantId !== null && !token(tenantId, 128)) ||
      !Array.isArray(eventIds) ||
      eventIds.length < 1 ||
      eventIds.length > 3 ||
      eventIds.some((id) => !token(id, 128)) ||
      new Set(eventIds).size !== eventIds.length
    )
      throw new HeimdallError("invalid_argument");
    const ids = [...eventIds];
    return transaction(
      this.pool,
      async (client) => {
        await client.query("SELECT pg_advisory_xact_lock(74162401)");
        await lockRuntimeCapacity(client);
        await client.query(
          "LOCK TABLE heimdall.audit_events,heimdall.audit_deliveries,heimdall.audit_decisions,heimdall_capacity.event_charges,heimdall_capacity.reservations IN SHARE MODE",
        );
        return this.retainIdentityGroupTx(client, tenantId, resolution, ids);
      },
      signal,
    );
  }
  private async retainIdentityGroupTx(
    client: PoolClient,
    tenantId: string | null,
    resolution: string,
    ids: string[],
  ) {
    const sources = [];
    for (const id of ids) {
      const result = await client.query(
        "SELECT e.event,e.recorded_at::text AS at,encode(e.payload_digest,'hex') AS digest,c.delivery_rows AS count,ARRAY(SELECT sink_id FROM heimdall.audit_deliveries d WHERE d.product_id=e.product_id AND d.environment=e.environment AND d.event_id=e.event_id ORDER BY sink_id COLLATE \"C\") AS routing FROM heimdall.audit_events e JOIN heimdall_capacity.event_charges c USING(product_id,environment,event_id) WHERE e.product_id=$1 AND e.environment=$2 AND e.event_id=$3 AND octet_length(e.event::text)<=1500000 AND c.envelope_bytes=octet_length(e.event::text)",
        [this.productId, this.environment, id],
      );
      const row = result.rows[0];
      if (!row) throw new HeimdallError("audit_unavailable");
      const event = this.decodeStored(row.event, id, Number(row.at), row.digest);
      sources.push({
        event,
        digest: row.digest,
        routing: row.routing,
        deliveryRows: Number(row.count),
      });
    }
    const identities = buildRetentionIdentities(
      { productId: this.productId, environment: this.environment, tenantId },
      resolution,
      sources,
    );
    for (const i of identities)
      await capacityWrite(
        client,
        "INSERT INTO heimdall_retention.event_identities(product_id,environment,event_id,tenant_id,recorded_at,payload_digest,kind,decision_id,routing) VALUES($1,$2,$3,$4,$5,decode($6,'hex'),$7,$8,$9::jsonb) ON CONFLICT(product_id,environment,event_id) DO UPDATE SET tenant_id=EXCLUDED.tenant_id,recorded_at=EXCLUDED.recorded_at,payload_digest=EXCLUDED.payload_digest,kind=EXCLUDED.kind,decision_id=EXCLUDED.decision_id,routing=EXCLUDED.routing",
        [
          i.productId,
          i.environment,
          i.eventId,
          i.tenantId,
          i.recordedAt,
          i.digest,
          i.kind,
          i.decisionId,
          JSON.stringify(i.routing),
        ],
      );
    if (resolution !== "ordinary") {
      const binding = bindAuditDecision(
        this.productId,
        this.environment,
        sources.map((s) => s.event).filter((e) => e.eventType !== "heimdall.recovery.completed"),
      );
      await capacityWrite(
        client,
        "INSERT INTO heimdall_retention.decision_identities(product_id,environment,decision_id,tenant_id,request_event_id,requested_at,resolution,terminal_event_id,recovery_event_id,resolved_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(product_id,environment,decision_id) DO UPDATE SET tenant_id=EXCLUDED.tenant_id,request_event_id=EXCLUDED.request_event_id,requested_at=EXCLUDED.requested_at,resolution=EXCLUDED.resolution,terminal_event_id=EXCLUDED.terminal_event_id,recovery_event_id=EXCLUDED.recovery_event_id,resolved_at=EXCLUDED.resolved_at",
        [
          this.productId,
          this.environment,
          binding.decisionId,
          tenantId,
          binding.requestEventId,
          binding.requestedAt,
          binding.resolution,
          binding.terminalEventId,
          identities.find((i) => i.kind === "recovery")?.eventId ?? null,
          binding.resolvedAt,
        ],
      );
    }
    return identities;
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
  private pendingReadSQL(sql: string) {
    if (!this.retainedRuntime) return sql;
    const readiness = retentionSQL.replace(/\$[1-6]/g, (x) => "$" + (Number(x.slice(1)) + 6));
    return sql.replace(
      "NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'",
      `(SELECT ready FROM (${readiness}) admission)`,
    );
  }
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
      this.pendingReadSQL(`
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
 (SELECT count(*)::int FROM selected) AS total,COALESCE(jsonb_agg(record ORDER BY requested_at,decision_id COLLATE "C"),'[]'::jsonb) AS records FROM sized WHERE size<=4194304`),
      [
        this.productId,
        this.environment,
        cutoff,
        after?.requestedAt ?? null,
        after?.decisionId ?? null,
        limit + 1,
        ...(this.retainedRuntime ? retentionArgs : []),
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
  async pending(sink: string, now: number, limit: number, signal?: AbortSignal, tenantId?: string) {
    if (
      !token(sink, 128) ||
      !Number.isSafeInteger(now) ||
      now < 0 ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100 ||
      (tenantId !== undefined && (!this.retainedRuntime || !token(tenantId, 128)))
    )
      throw new HeimdallError("invalid_argument");
    type PendingRow = {
      event_id: string;
      attempts: number;
      recorded_at: string;
      payload_digest: Buffer;
      event: Record<string, unknown>;
    };
    const statement = `WITH candidates AS MATERIALIZED (
      SELECT d.event_id,d.attempts,d.next_attempt_at,e.recorded_at,e.payload_digest,e.event
      FROM heimdall.audit_deliveries d JOIN heimdall.audit_events e USING(product_id,environment,event_id)
      WHERE d.product_id=$1 AND d.environment=$2 AND d.sink_id=$3 AND d.state='pending' AND d.next_attempt_at<=$4
      ${tenantId === undefined ? "" : "AND EXISTS (SELECT 1 FROM heimdall_retention.event_identities i WHERE i.product_id=d.product_id AND i.environment=d.environment AND i.event_id=d.event_id AND i.tenant_id=$6)"}
      ORDER BY d.next_attempt_at,d.event_id COLLATE "C" LIMIT $5
    ), sized AS (
      SELECT *,sum(octet_length(event::text)) OVER(ORDER BY next_attempt_at,event_id COLLATE "C") AS size FROM candidates
    ) SELECT event_id,attempts,recorded_at::text,payload_digest,event FROM sized WHERE size<=4194304 ORDER BY next_attempt_at,event_id COLLATE "C"`;
    const args = [this.productId, this.environment, sink, now, limit];
    if (tenantId !== undefined) args.push(tenantId);
    const rows = this.retainedRuntime
      ? await readSnapshot(
          this.pool,
          async (client) => {
            const ready = await client.query<{ ready: boolean }>(retentionSQL, retentionArgs);
            if (ready.rows[0]?.ready !== true) throw new HeimdallError("unavailable");
            return (await client.query<PendingRow>(statement, args)).rows;
          },
          signal,
        )
      : await query<PendingRow>(this.pool, statement, args, false, signal);
    return rows.map((row) => {
      let event: Record<string, unknown> | null = null;
      try {
        event = this.decodeStored(
          row.event,
          row.event_id,
          Number(row.recorded_at),
          row.payload_digest.toString("hex"),
        );
        if (tenantId !== undefined && event.tenantId !== tenantId) event = null;
      } catch {
        /* Return explicit invalid record identity for quarantine; never export its payload. */
      }
      return { eventId: row.event_id, attempts: row.attempts, event };
    });
  }
  // One primary snapshot; this returns durable history, never fresh permission.
  async readResolution(decisionId: string, signal?: AbortSignal) {
    if (!token(decisionId, 128)) throw new HeimdallError("invalid_argument");
    const rows = await query<{
      ready: boolean;
      record: {
        id: string;
        requestedAt: string;
        resolution: string;
        terminalId: string | null;
        resolvedAt: string | null;
        reservationRequest: string | null;
        reservationResolution: string | null;
        reservationTerminal: string | null;
        recoveryId: string | null;
        request: {
          event_id: string;
          recorded_at: string;
          payload_digest: string;
          event: Record<string, unknown>;
        } | null;
        terminal: {
          event_id: string;
          recorded_at: string;
          payload_digest: string;
          event: Record<string, unknown>;
        } | null;
        administration: {
          event_id: string;
          recorded_at: string;
          payload_digest: string;
          event: Record<string, unknown>;
        } | null;
      } | null;
    }>(
      this.pool,
      resolutionSQL,
      [
        sessionChecksum,
        decisionChecksum,
        capacityChecksum,
        capacityPreparationChecksum,
        this.productId,
        this.environment,
        decisionId,
      ],
      false,
      signal,
    );
    if (rows.length !== 1 || rows[0]?.ready !== true) throw new HeimdallError("unavailable");
    const r = rows[0].record;
    if (r === null) return null;
    const number = (s: string) => {
      const n = Number(s);
      if (!Number.isSafeInteger(n) || n < 0 || String(n) !== s)
        throw new HeimdallError("audit_unavailable");
      return n;
    };
    const decode = (e: typeof r.request) => {
      if (!e || !token(e.event_id, 128) || !/^[a-f0-9]{64}(?![\s\S])/.test(e.payload_digest))
        throw new HeimdallError("audit_unavailable");
      return this.decodeStored(e.event, e.event_id, number(e.recorded_at), e.payload_digest);
    };
    const request = decode(r.request);
    const terminal = r.terminalId === null ? null : decode(r.terminal);
    if (r.terminalId === null && r.terminal !== null) throw new HeimdallError("audit_unavailable");
    const b = bindAuditDecision(
      this.productId,
      this.environment,
      terminal ? [request, terminal] : [request],
    );
    if (
      r.id !== decisionId ||
      b.decisionId !== decisionId ||
      b.requestedAt !== number(r.requestedAt) ||
      b.resolution !== r.resolution ||
      b.terminalEventId !== r.terminalId ||
      b.resolvedAt !== (r.resolvedAt === null ? null : number(r.resolvedAt)) ||
      r.reservationRequest !== b.requestEventId ||
      r.reservationResolution !== r.resolution ||
      r.reservationTerminal !== r.terminalId
    )
      throw new HeimdallError("audit_unavailable");
    let administration: Record<string, unknown> | null = null;
    if (r.resolution === "recovered") {
      if (
        !terminal ||
        r.recoveryId !== recoveryEventId(this.productId, this.environment, r.terminalId!)
      )
        throw new HeimdallError("audit_unavailable");
      administration = decode(r.administration);
      try {
        validateRecoveryPair(terminal, administration, b.requestedAt);
      } catch {
        throw new HeimdallError("audit_unavailable");
      }
    } else if (r.recoveryId !== null || r.administration !== null)
      throw new HeimdallError("audit_unavailable");
    return { decisionId, state: r.resolution, request, terminal, administration };
  }
  // Internal prepared port used by migration tests; public writes require v3.
  async appendCapacity(event: Record<string, unknown>, signal?: AbortSignal) {
    return this.appendCapacityWith(event, lockAuditCapacity, signal);
  }
  private async appendCapacityWith(
    event: Record<string, unknown>,
    admit: (client: PoolClient) => Promise<void>,
    signal?: AbortSignal,
  ) {
    if (
      event?.eventType === "heimdall.authorization.completed" &&
      event.outcome === "outcome_unknown"
    )
      throw new HeimdallError("invalid_argument");
    return this.appendFinalized(
      event,
      admit,
      async (client, snapshot, inserted) => {
        await this.finalizeCapacityEvent(client, snapshot, inserted);
      },
      signal,
    );
  }
  private async finalizeCapacityEvent(
    client: PoolClient,
    snapshot: Record<string, unknown>,
    inserted: boolean,
  ) {
    const scope = [this.productId, this.environment];
    const request = snapshot.eventType === "heimdall.authorization.requested";
    const completion = snapshot.eventType === "heimdall.authorization.completed";
    if (request || completion) {
      const changed = await this.persistDecision(client, snapshot);
      if (completion) await resolveAuditCapacity(client, scope, snapshot, changed, null);
    }
    await chargeAuditEvent(client, scope, snapshot.eventId as string, inserted);
    if (request) await reserveAuditDecision(client, scope, snapshot, inserted);
    if (request || completion) await checkAuditReservation(client, scope, snapshot, null);
  }
  async recoverCapacity(
    terminal: Record<string, unknown>,
    administration: Record<string, unknown>,
    cutoff: number,
    signal?: AbortSignal,
  ) {
    return this.recoverCapacityWith(terminal, administration, cutoff, lockAuditCapacity, signal);
  }
  // Trusted maintenance port; the caller authorizes the recovery initiator.
  async recoverRuntime(
    terminal: Record<string, unknown>,
    administration: Record<string, unknown>,
    cutoff: number,
    signal?: AbortSignal,
  ) {
    return this.recoverCapacityWith(terminal, administration, cutoff, lockRuntimeCapacity, signal);
  }
  private async recoverCapacityWith(
    terminal: Record<string, unknown>,
    administration: Record<string, unknown>,
    cutoff: number,
    admit: (client: PoolClient) => Promise<void>,
    signal?: AbortSignal,
  ) {
    const completion = this.prepare(terminal),
      recovery = this.prepare(administration);
    validateRecoveryPair(completion.snapshot, recovery.snapshot, cutoff);
    const scope = [this.productId, this.environment];
    const receipt = await this.appendFinalized(
      completion.snapshot,
      admit,
      async (client, snapshot, inserted) => {
        const changed = await this.persistDecision(client, snapshot, cutoff);
        const adminInserted = await this.writePrepared(client, recovery, !changed);
        await resolveAuditCapacity(client, scope, snapshot, changed, recovery.eventId);
        await chargeAuditEvent(client, scope, completion.eventId, inserted);
        await chargeAuditEvent(client, scope, recovery.eventId, adminInserted);
        await checkAuditReservation(client, scope, snapshot, recovery.eventId);
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
  // Prepared compatible writer only. Public v4 activation remains gated.
  async appendRetained(event: Record<string, unknown>, signal?: AbortSignal) {
    return this.appendRetainedWith(event, async () => {}, signal);
  }
  // Trusted SDK-local mutations must enforce their own idempotency and generation.
  private async appendRetainedWith(
    event: Record<string, unknown>,
    mutate: (client: PoolClient) => Promise<void>,
    signal?: AbortSignal,
  ) {
    return this.appendRetainedFinalized(event, mutate, async () => {}, signal);
  }
  private async appendRetainedFinalized(
    event: Record<string, unknown>,
    mutate: (client: PoolClient) => Promise<void>,
    finalize: (client: PoolClient) => Promise<void>,
    signal?: AbortSignal,
  ) {
    if (typeof mutate !== "function" || typeof finalize !== "function")
      throw new HeimdallError("invalid_argument");
    const p = this.prepare(event);
    return transaction(
      this.pool,
      async (client) => {
        await this.lockRetainedCapacity(client);
        await mutate(client);
        const receipt = await this.appendPreparedRetained(client, p);
        await finalize(client);
        return receipt;
      },
      signal,
    );
  }

  // Trusted collector calls this after validating a complete source window.
  private async appendIdentityWindow(
    events: readonly Record<string, unknown>[],
    advance: (client: PoolClient) => Promise<void>,
    signal: AbortSignal,
  ) {
    if (
      events.length > 256 ||
      events.some(
        (event) => event.source !== "zitadel" || event.eventType !== "heimdall.identity.observed",
      )
    )
      throw new HeimdallError("invalid_argument");
    const prepared = events.map((event) => this.prepare(event));
    await transaction(
      this.pool,
      async (client) => {
        await this.lockRetainedCapacity(client);
        await advance(client);
        for (const event of prepared) await this.appendPreparedRetained(client, event);
      },
      signal,
    );
  }

  private async appendPreparedRetained(
    client: PoolClient,
    p: ReturnType<PostgresAudit["prepare"]>,
  ) {
    const receipt = {
      productId: this.productId,
      environment: this.environment,
      eventId: p.eventId,
      recordedAt: p.recordedAt,
      payloadDigest: p.originalDigest,
    };
    const tenant = (p.snapshot.tenantId ?? null) as string | null;
    const read = (id: string) => this.readRetainedTx(client, id);
    const existing = await read(p.eventId);
    if (existing) {
      if (
        existing.identity.digest !== p.originalDigest ||
        existing.identity.recordedAt !== p.recordedAt
      )
        throw new HeimdallError("conflict");
      return receipt;
    }
    const request = p.snapshot.eventType === "heimdall.authorization.requested";
    const completion = p.snapshot.eventType === "heimdall.authorization.completed";
    if (
      (completion && p.snapshot.outcome === "outcome_unknown") ||
      (p.snapshot.eventType === "heimdall.recovery.completed" &&
        p.snapshot.action === "authorization.recover")
    )
      throw new HeimdallError("invalid_argument");
    let resolution = "ordinary";
    let ids = [p.eventId];
    if (request || completion) {
      const result = await client.query(
        "SELECT request_event_id,resolution FROM heimdall_retention.decision_identities WHERE product_id=$1 AND environment=$2 AND decision_id=$3",
        [this.productId, this.environment, p.snapshot.decisionId],
      );
      const d = result.rows[0];
      if (request) {
        if (d) throw new HeimdallError("conflict");
        resolution = "pending";
      } else {
        if (!d) throw new HeimdallError("audit_unavailable");
        if (d.resolution !== "pending") throw new HeimdallError("conflict");
        const original = await read(d.request_event_id);
        if (!original || original.state !== "live" || original.identity.tenantId !== tenant)
          throw new HeimdallError("audit_unavailable");
        resolution = "completed";
        ids = [d.request_event_id, p.eventId];
      }
    }
    const inserted = await this.writePrepared(client, p);
    if (!inserted) throw new HeimdallError("audit_unavailable");
    await this.finalizeCapacityEvent(client, p.snapshot, inserted);
    await this.retainIdentityGroupTx(client, tenant, resolution, ids);
    return receipt;
  }
  // Privileged persistence. Trusted orchestration supplies authorization and the policy clock.
  private async retire(
    request: AuditRetirementRequest,
    administration: Record<string, unknown>,
    signal?: AbortSignal,
  ) {
    const r = { ...request };
    if (r.productId !== this.productId || r.environment !== this.environment)
      throw new HeimdallError("invalid_argument");
    const p = this.prepare(administration);
    validateAuditRetirement(r, p.snapshot);
    const marker = [
      r.productId,
      r.environment,
      r.retirementId,
      r.eventId,
      r.tenantId,
      r.at,
      r.retainForMs,
      p.eventId,
    ];
    let candidate: ReturnType<PostgresAudit["decodeRetirementCandidate"]> = null;
    return this.appendRetainedFinalized(
      p.snapshot,
      async (client) => {
        const previous = await client.query<{ same: boolean }>(
          `SELECT retirement_id=$3 AND target_event_id=$4 AND tenant_id IS NOT DISTINCT FROM $5::text
            AND retired_at=$6 AND retain_for_ms=$7 AND administration_event_id=$8 AS same
            FROM heimdall_retention.retirements WHERE product_id=$1 AND environment=$2
            AND (retirement_id=$3 OR (target_event_id=$4 AND tenant_id IS NOT DISTINCT FROM $5::text))`,
          marker,
        );
        const prior = await this.readRetainedTx(client, p.eventId);
        if (previous.rows.length) {
          if (previous.rows.length !== 1 || previous.rows[0]?.same !== true)
            throw new HeimdallError("conflict");
          const root = await this.readRetainedTx(client, r.eventId);
          if (!prior || root?.state !== "retired" || root.identity.tenantId !== r.tenantId)
            throw new HeimdallError("audit_unavailable");
          return;
        }
        if (prior) throw new HeimdallError("audit_unavailable");
        const result = await client.query<RetentionEligibilityRow>(
          this.retainedHistorySQL(retentionEligibilitySQL),
          this.retainedHistoryArgs(r.eventId),
        );
        if (result.rows.length !== 1) throw new HeimdallError("unavailable");
        candidate = this.decodeRetirementCandidate(
          result.rows[0]!,
          r.tenantId,
          r.eventId,
          r.at,
          r.retainForMs,
        );
        if (!candidate) throw new HeimdallError("not_found");
        if (candidate.reason !== "candidate") throw new HeimdallError("conflict");
      },
      async (client) => {
        if (!candidate) return;
        await capacityWrite(
          client,
          `INSERT INTO heimdall_retention.retirements
          (product_id,environment,retirement_id,target_event_id,tenant_id,retired_at,retain_for_ms,administration_event_id,member_event_ids)
          VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)`,
          [...marker, JSON.stringify(candidate.ids)],
        );
        const remove = async (
          table: string,
          where: string,
          key: unknown,
          expected: number | null,
        ) => {
          const result = await capacityWrite(
            client,
            `DELETE FROM ${table} WHERE product_id=$1 AND environment=$2 AND ${where}`,
            [r.productId, r.environment, key],
          );
          if (expected !== null && result.rowCount !== expected)
            throw new HeimdallError("audit_unavailable");
        };
        if (candidate.decisionId !== null) {
          for (const table of ["heimdall_capacity.reservations", "heimdall.audit_decisions"])
            await remove(table, "decision_id=$3", candidate.decisionId, 1);
        }
        for (const table of [
          "heimdall.audit_deliveries",
          "heimdall_capacity.event_charges",
          "heimdall.audit_events",
        ])
          await remove(
            table,
            "event_id=ANY($3)",
            candidate.ids,
            table === "heimdall.audit_deliveries" ? null : candidate.ids.length,
          );
      },
      signal,
    );
  }
  // Privileged internal persistence; trusted orchestration supplies actor, policy and release authority.
  private async applyHold(
    request: AuditHoldRequest,
    administration: Record<string, unknown>,
    signal?: AbortSignal,
  ) {
    const r = { ...request };
    if (r.productId !== this.productId || r.environment !== this.environment)
      throw new HeimdallError("invalid_argument");
    const p = this.prepare(administration);
    validateAuditHold(r, p.snapshot);
    let change = false;
    return this.appendRetainedFinalized(
      p.snapshot,
      async (client) => {
        const rows = await client.query(
          "SELECT event_id,tenant_id,purpose,placed_at::text,placement_event_id,released_at::text,release_event_id FROM heimdall_retention.event_holds WHERE product_id=$1 AND environment=$2 AND hold_id=$3 FOR UPDATE",
          [this.productId, this.environment, r.holdId],
        );
        const h = rows.rows[0];
        if (
          h &&
          (h.event_id !== r.eventId || h.tenant_id !== r.tenantId || h.purpose !== r.purpose)
        )
          throw new HeimdallError("conflict");
        if (r.operation === "acquire" && h) {
          if (h.placed_at !== String(r.at) || h.placement_event_id !== p.eventId)
            throw new HeimdallError("conflict");
        } else if (r.operation === "release") {
          if (!h) throw new HeimdallError("not_found");
          if (r.at < Number(h.placed_at)) throw new HeimdallError("invalid_argument");
          if (
            h.released_at !== null &&
            (h.released_at !== String(r.at) || h.release_event_id !== p.eventId)
          )
            throw new HeimdallError("conflict");
        }
        change = !h || (r.operation === "release" && h.released_at === null);
        const prior = await this.readRetainedTx(client, p.eventId);
        if ((change && prior) || (!change && !prior)) throw new HeimdallError("audit_unavailable");
        if (!change) return;
        const root = await this.readRetainedTx(client, r.eventId);
        if (!root || root.identity.tenantId !== r.tenantId || root.state === "retired")
          throw new HeimdallError("not_found");
        if (r.at < root.identity.recordedAt) throw new HeimdallError("invalid_argument");
        let resolution = "ordinary";
        let ids = [r.eventId];
        if (root.identity.kind !== "ordinary") {
          const result = await client.query(holdDecisionSQL, [
            this.productId,
            this.environment,
            root.identity.decisionId,
          ]);
          const d = result.rows[0];
          if (!d) throw new HeimdallError("audit_unavailable");
          resolution = d.resolution;
          ids = d.ids;
        }
        if (ids.length < 1 || ids.length > 3) throw new HeimdallError("audit_unavailable");
        const locked = await client.query(
          'SELECT event_id FROM heimdall_retention.event_identities WHERE product_id=$1 AND environment=$2 AND event_id=ANY($3) ORDER BY event_id COLLATE "C" FOR UPDATE',
          [this.productId, this.environment, ids],
        );
        if (locked.rows.length !== ids.length) throw new HeimdallError("audit_unavailable");
        const sources = [];
        for (const id of ids) {
          const member = await this.readRetainedTx(client, id);
          if (!member || member.state !== "live" || member.identity.tenantId !== r.tenantId)
            throw new HeimdallError("audit_unavailable");
          sources.push({
            event: member.event,
            digest: member.identity.digest,
            routing: member.identity.routing,
            deliveryRows: member.identity.routing.length,
          });
        }
        buildRetentionIdentities(
          { productId: this.productId, environment: this.environment, tenantId: r.tenantId },
          resolution,
          sources,
        );
        if (r.operation === "release") {
          const protocol = await client.query<{ database_version: number }>(
            "SELECT database_version FROM heimdall.schema_metadata WHERE singleton",
          );
          if (protocol.rows[0]?.database_version === 5) {
            const consumed = await capacityWrite(
              client,
              "UPDATE heimdall_retention.hold_release_reservations SET release_event_id=$4 WHERE product_id=$1 AND environment=$2 AND hold_id=$3 AND release_event_id IS NULL RETURNING hold_id",
              [this.productId, this.environment, r.holdId, p.eventId],
            );
            if (consumed.rowCount !== 1) throw new HeimdallError("audit_unavailable");
          }
        }
      },
      async (client) => {
        if (!change) return;
        if (r.operation === "acquire")
          await capacityWrite(
            client,
            "INSERT INTO heimdall_retention.event_holds(product_id,environment,hold_id,event_id,tenant_id,purpose,placed_at,placement_event_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)",
            [
              this.productId,
              this.environment,
              r.holdId,
              r.eventId,
              r.tenantId,
              r.purpose,
              r.at,
              p.eventId,
            ],
          );
        else
          await capacityWrite(
            client,
            "UPDATE heimdall_retention.event_holds SET released_at=$4,release_event_id=$5 WHERE product_id=$1 AND environment=$2 AND hold_id=$3",
            [this.productId, this.environment, r.holdId, r.at, p.eventId],
          );
      },
      signal,
    );
  }
  private async lockRetainedCapacity(client: PoolClient) {
    if (this.retainedRuntime) {
      await lockRetentionAdmission(client);
      await lockAuditCapacity(client);
    } else await lockRuntimeCapacity(client);
    const result = await client.query(
      "SELECT singleton FROM heimdall_retention.capacity WHERE singleton FOR UPDATE",
    );
    if (result.rows.length !== 1) throw new HeimdallError("audit_unavailable");
  }
  private async readRetainedTx(client: PoolClient, id: string) {
    const rows = await client.query<RetainedRow>(
      this.retainedHistorySQL(eligibilityReadSQL),
      this.retainedHistoryArgs(id),
    );
    if (rows.rows.length !== 1) throw new HeimdallError("unavailable");
    return this.decodeRetainedRow(rows.rows[0]!, null, id, false);
  }
  // Trusted internal recovery: the caller authenticates and authorizes its initiator.
  async recoverRetained(
    terminal: Record<string, unknown>,
    administration: Record<string, unknown>,
    cutoff: number,
    signal?: AbortSignal,
  ) {
    const t = this.prepare(terminal),
      a = this.prepare(administration);
    validateRecoveryPair(t.snapshot, a.snapshot, cutoff);
    const receipt = (p: typeof t) => ({
      productId: this.productId,
      environment: this.environment,
      eventId: p.eventId,
      recordedAt: p.recordedAt,
      payloadDigest: p.originalDigest,
    });
    const tenant = (t.snapshot.tenantId ?? null) as string | null;
    return transaction(
      this.pool,
      async (client) => {
        await this.lockRetainedCapacity(client);
        const completed = await this.readRetainedTx(client, t.eventId);
        const recovered = await this.readRetainedTx(client, a.eventId);
        for (const [stored, p] of [
          [completed, t],
          [recovered, a],
        ] as const) {
          if (
            stored &&
            (stored.identity.digest !== p.originalDigest ||
              stored.identity.recordedAt !== p.recordedAt)
          )
            throw new HeimdallError("conflict");
        }
        const rows = await client.query(
          "SELECT request_event_id,requested_at::text,resolution,terminal_event_id,recovery_event_id FROM heimdall_retention.decision_identities WHERE product_id=$1 AND environment=$2 AND decision_id=$3",
          [this.productId, this.environment, t.snapshot.decisionId],
        );
        const d = rows.rows[0];
        if (!d) throw new HeimdallError("audit_unavailable");
        if (Number(d.requested_at) > cutoff) throw new HeimdallError("conflict");
        const request = await this.readRetainedTx(client, d.request_event_id);
        if (!request || request.identity.tenantId !== tenant)
          throw new HeimdallError("audit_unavailable");
        if (completed || recovered) {
          if (
            !completed ||
            !recovered ||
            d.resolution !== "recovered" ||
            d.terminal_event_id !== t.eventId ||
            d.recovery_event_id !== a.eventId
          )
            throw new HeimdallError("audit_unavailable");
          if (request.state !== completed.state || request.state !== recovered.state)
            throw new HeimdallError("audit_unavailable");
          if (request.state === "live") {
            buildRetentionIdentities(
              { productId: this.productId, environment: this.environment, tenantId: tenant },
              "recovered",
              [request, completed, recovered].map((member) => ({
                event: member.event!,
                digest: member.identity.digest,
                routing: member.identity.routing,
                deliveryRows: member.identity.routing.length,
              })),
            );
            await checkAuditReservation(
              client,
              [this.productId, this.environment],
              t.snapshot,
              a.eventId,
            );
          }
        } else {
          if (d.resolution !== "pending") throw new HeimdallError("conflict");
          if (request.state !== "live") throw new HeimdallError("audit_unavailable");
          const inserted = await this.writePrepared(client, t);
          const changed = await this.persistDecision(client, t.snapshot, cutoff);
          const adminInserted = await this.writePrepared(client, a);
          if (!inserted || !changed || !adminInserted) throw new HeimdallError("audit_unavailable");
          const scope = [this.productId, this.environment];
          await resolveAuditCapacity(client, scope, t.snapshot, changed, a.eventId);
          await chargeAuditEvent(client, scope, t.eventId, inserted);
          await chargeAuditEvent(client, scope, a.eventId, adminInserted);
          await checkAuditReservation(client, scope, t.snapshot, a.eventId);
          await this.retainIdentityGroupTx(client, tenant, "recovered", [
            d.request_event_id,
            t.eventId,
            a.eventId,
          ]);
        }
        return { terminal: receipt(t), administration: receipt(a) };
      },
      signal,
    );
  }
  // Public writes hold v3 admission and capacity accounting through COMMIT.
  async appendRuntime(event: Record<string, unknown>, signal?: AbortSignal) {
    return this.appendCapacityWith(event, lockRuntimeCapacity, signal);
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
    finalize: (
      client: PoolClient,
      event: Record<string, unknown>,
      inserted: boolean,
    ) => Promise<void>,
    signal?: AbortSignal,
  ) {
    if (typeof mutate !== "function") throw new HeimdallError("invalid_argument");
    const prepared = this.prepare(event);
    return transaction(
      this.pool,
      async (client) => {
        await mutate(client);
        const { snapshot, eventId, recordedAt, originalDigest } = prepared;
        const inserted = await this.writePrepared(client, prepared);
        await finalize(client, snapshot, inserted);
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
    return inserted.rowCount === 1;
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
        if (this.retainedRuntime) await lockRetentionAdmission(client);
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
        if (this.retainedRuntime) await lockRetentionAdmission(client);
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
        if (this.retainedRuntime) await lockRetentionAdmission(client);
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
        if (this.retainedRuntime) await lockRetentionAdmission(client);
        const rows = await client.query(
          "UPDATE heimdall.audit_deliveries SET state='quarantined' WHERE product_id=$1 AND environment=$2 AND sink_id=$3 AND event_id=$4 AND attempts=$5 AND state IN ('pending','quarantined') RETURNING attempts",
          args,
        );
        return rows.rowCount === 1;
      },
      signal,
    );
  }

  // Advancing the counter invalidates callbacks from before quarantine.
  async requeueQuarantined(
    sink: string,
    eventId: string,
    expectedAttempts: number,
    retryAt: number,
    signal?: AbortSignal,
    tenantId?: string,
  ): Promise<boolean> {
    const args = this.deliveryArgs(sink, eventId, expectedAttempts);
    if (
      (tenantId !== undefined && (!this.retainedRuntime || !token(tenantId, 128))) ||
      expectedAttempts >= 2147483646 ||
      !Number.isSafeInteger(retryAt) ||
      retryAt < 0
    )
      throw new HeimdallError("invalid_argument");
    return transaction(
      this.pool,
      async (client) => {
        if (this.retainedRuntime) await lockRetentionAdmission(client);
        const rows = await client.query(
          "UPDATE heimdall.audit_deliveries d SET state='pending',attempts=attempts+1,next_attempt_at=$6 WHERE product_id=$1 AND environment=$2 AND sink_id=$3 AND event_id=$4 AND attempts=$5 AND state='quarantined'" +
            (tenantId === undefined
              ? ""
              : " AND EXISTS (SELECT 1 FROM heimdall_retention.event_identities i WHERE i.product_id=d.product_id AND i.environment=d.environment AND i.event_id=d.event_id AND i.tenant_id=$7)") +
            " RETURNING attempts",
          tenantId === undefined ? [...args, retryAt] : [...args, retryAt, tenantId],
        );
        return rows.rowCount === 1;
      },
      signal,
    );
  }
}

const resolutionSQL = `SELECT readiness.ready, CASE WHEN d.decision_id IS NULL THEN NULL ELSE jsonb_build_object(
 'id',d.decision_id,'requestedAt',d.requested_at::text,'resolution',d.resolution,'terminalId',d.terminal_event_id,'resolvedAt',d.resolved_at::text,
 'reservationRequest',c.request_event_id,'reservationResolution',c.resolution,'reservationTerminal',c.terminal_event_id,'recoveryId',c.recovery_event_id,
 'request',CASE WHEN r.event_id IS NULL THEN NULL ELSE jsonb_build_object('event_id',r.event_id,'recorded_at',r.recorded_at::text,'payload_digest',encode(r.payload_digest,'hex'),'event',r.event) END,'terminal',CASE WHEN t.event_id IS NULL THEN NULL ELSE jsonb_build_object('event_id',t.event_id,'recorded_at',t.recorded_at::text,'payload_digest',encode(t.payload_digest,'hex'),'event',t.event) END,'administration',CASE WHEN a.event_id IS NULL THEN NULL ELSE jsonb_build_object('event_id',a.event_id,'recorded_at',a.recorded_at::text,'payload_digest',encode(a.payload_digest,'hex'),'event',a.event) END) END AS record
 FROM (${capacitySQL}) readiness
 LEFT JOIN heimdall.audit_decisions d ON d.product_id=$5 AND d.environment=$6 AND d.decision_id=$7
 LEFT JOIN heimdall_capacity.reservations c ON c.product_id=d.product_id AND c.environment=d.environment AND c.decision_id=d.decision_id
 LEFT JOIN heimdall.audit_events r ON r.product_id=d.product_id AND r.environment=d.environment AND r.event_id=d.request_event_id
 LEFT JOIN heimdall.audit_events t ON t.product_id=d.product_id AND t.environment=d.environment AND t.event_id=d.terminal_event_id
 LEFT JOIN heimdall.audit_events a ON a.product_id=d.product_id AND a.environment=d.environment AND a.event_id=c.recovery_event_id`;

const retainedReadSQL = `SELECT readiness.ready, i.event_id IS NOT NULL AS present, e.event_id IS NOT NULL AS live,
 coalesce(m.marker_count,0) AS markers,
 i.tenant_id,
 CASE WHEN i.event_id IS NULL THEN NULL ELSE jsonb_build_object('productId',i.product_id,'environment',i.environment,'tenantId',i.tenant_id,'eventId',i.event_id,'recordedAt',i.recorded_at,'digest',encode(i.payload_digest,'hex'),'kind',i.kind,'decisionId',i.decision_id,'routing',i.routing) END AS identity,
 e.event AS payload,
 coalesce(
  (i.kind='ordinary' OR (d.tenant_id IS NOT DISTINCT FROM i.tenant_id AND CASE i.kind WHEN 'request' THEN d.request_event_id=i.event_id WHEN 'terminal' THEN d.terminal_event_id=i.event_id WHEN 'recovery' THEN d.recovery_event_id=i.event_id ELSE false END))
  AND CASE WHEN e.event_id IS NOT NULL THEN
   m.retirement_id IS NULL AND e.recorded_at=i.recorded_at AND e.payload_digest=i.payload_digest
   AND c.envelope_bytes=octet_length(e.event::text) AND c.delivery_rows=jsonb_array_length(i.routing)
   AND i.routing=to_jsonb(ARRAY(SELECT sink_id FROM heimdall.audit_deliveries x WHERE x.product_id=$5 AND x.environment=$6 AND x.event_id=$7 ORDER BY sink_id COLLATE "C"))
  ELSE
   m.retirement_id IS NOT NULL AND m.tenant_id IS NOT DISTINCT FROM i.tenant_id
   AND m.target_event_id=CASE WHEN i.kind='ordinary' THEN i.event_id ELSE d.request_event_id END
   AND (i.kind='ordinary' OR d.resolution IN ('completed','recovered'))
   AND m.member_event_ids=CASE WHEN i.kind='ordinary' THEN jsonb_build_array(i.event_id) ELSE
    to_jsonb(ARRAY(SELECT id FROM unnest(array_remove(ARRAY[d.request_event_id,d.terminal_event_id,d.recovery_event_id],NULL)) id ORDER BY id COLLATE "C")) END
   AND m.retired_at>=i.recorded_at+m.retain_for_ms
   AND EXISTS(SELECT 1 FROM heimdall_retention.event_identities a WHERE a.product_id=$5 AND a.environment=$6 AND a.event_id=m.administration_event_id AND a.tenant_id IS NOT DISTINCT FROM i.tenant_id AND a.recorded_at=m.retired_at)
   AND NOT EXISTS(SELECT 1 FROM heimdall.audit_events remaining WHERE remaining.product_id=$5 AND remaining.environment=$6
    AND remaining.event_id=ANY(ARRAY[i.event_id,d.request_event_id,d.terminal_event_id,d.recovery_event_id]))
  END,false) AS valid
FROM (${capacitySQL}) readiness
LEFT JOIN heimdall_retention.event_identities i ON i.product_id=$5 AND i.environment=$6 AND i.event_id=$7
LEFT JOIN heimdall.audit_events e ON e.product_id=$5 AND e.environment=$6 AND e.event_id=$7
LEFT JOIN heimdall_capacity.event_charges c ON c.product_id=$5 AND c.environment=$6 AND c.event_id=$7
LEFT JOIN heimdall_retention.decision_identities d ON d.product_id=i.product_id AND d.environment=i.environment AND d.decision_id=i.decision_id
LEFT JOIN LATERAL (
 SELECT matches.*, count(*) OVER ()::int AS marker_count
 FROM (SELECT r.* FROM heimdall_retention.retirements r WHERE r.product_id=$5 AND r.environment=$6 AND r.member_event_ids ? $7 LIMIT 2) matches
 LIMIT 1
) m ON true
`;

const eligibilityReadSQL = retainedReadSQL.replace(
  "e.event AS payload",
  "CASE WHEN octet_length(e.event::text)<=1500000 THEN e.event ELSE NULL END AS payload",
);
const retentionEligibilitySQL = `SELECT to_jsonb(root) AS root,
 CASE WHEN root.identity->>'kind'='ordinary' THEN 'ordinary' ELSE d.resolution END AS resolution,
 coalesce(group_rows.members,'[]'::jsonb) AS members,
 coalesce(root.identity->>'kind'='ordinary' OR (
  (d.request_event_id,d.requested_at,d.resolution,d.terminal_event_id,d.resolved_at) IS NOT DISTINCT FROM (source.request_event_id,source.requested_at,source.resolution,source.terminal_event_id,source.resolved_at)
  AND (d.request_event_id,d.resolution,d.terminal_event_id,d.recovery_event_id) IS NOT DISTINCT FROM (reservation.request_event_id,reservation.resolution,reservation.terminal_event_id,reservation.recovery_event_id)
  AND source.decision_id IS NOT NULL AND reservation.decision_id IS NOT NULL
  AND d.requested_at=(root.identity->>'recordedAt')::bigint
  AND (d.resolution='pending' OR EXISTS(SELECT 1 FROM heimdall_retention.event_identities terminal
   WHERE terminal.product_id=d.product_id AND terminal.environment=d.environment AND terminal.event_id=d.terminal_event_id AND terminal.recorded_at=d.resolved_at))),false) AS bound,
 EXISTS(SELECT 1 FROM heimdall_retention.event_holds h WHERE h.product_id=$5 AND h.environment=$6 AND h.event_id=ANY(chosen.ids) AND h.purpose='administrative' AND h.released_at IS NULL) AS held,
 EXISTS(SELECT 1 FROM heimdall_retention.event_holds h WHERE h.product_id=$5 AND h.environment=$6 AND h.event_id=ANY(chosen.ids) AND h.purpose='retry' AND h.released_at IS NULL) AS retry
FROM (${eligibilityReadSQL}) root
LEFT JOIN heimdall_retention.decision_identities d ON d.product_id=$5 AND d.environment=$6 AND d.decision_id=root.identity->>'decisionId'
LEFT JOIN heimdall.audit_decisions source ON source.product_id=d.product_id AND source.environment=d.environment AND source.decision_id=d.decision_id
LEFT JOIN heimdall_capacity.reservations reservation ON reservation.product_id=d.product_id AND reservation.environment=d.environment AND reservation.decision_id=d.decision_id
CROSS JOIN LATERAL (SELECT CASE WHEN root.identity->>'kind'='ordinary' THEN ARRAY[$7::text] WHEN root.identity->>'kind'='request' THEN array_remove(ARRAY[d.request_event_id,d.terminal_event_id,d.recovery_event_id],NULL) ELSE ARRAY[]::text[] END AS ids) chosen
LEFT JOIN LATERAL (
 SELECT jsonb_agg(jsonb_build_object('eventId',member.event_id,'row',to_jsonb(history),'deliveries',coalesce(delivery.rows,'[]'::jsonb)) ORDER BY member.event_id COLLATE "C") AS members
 FROM unnest(chosen.ids) member(event_id)
 CROSS JOIN LATERAL (${eligibilityReadSQL.replaceAll("$7", "member.event_id")}) history
 LEFT JOIN LATERAL (SELECT jsonb_agg(jsonb_build_object('sinkId',sink_id,'state',state,'deliveredAt',delivered_at) ORDER BY sink_id COLLATE "C") AS rows FROM (SELECT sink_id,state,delivered_at FROM heimdall.audit_deliveries WHERE product_id=$5 AND environment=$6 AND event_id=member.event_id LIMIT 33) bounded) delivery ON true
) group_rows ON true
`;

const retirementPageSQL = `
WITH scanned AS MATERIALIZED (
 SELECT event_id FROM heimdall.audit_events
 WHERE product_id=$7 AND environment=$8 AND event_id>$9
 ORDER BY event_id LIMIT $10
)
SELECT admission.ready, (SELECT count(*)::int FROM scanned) AS scanned,
 (SELECT max(event_id) FROM scanned) AS last_scanned,
 EXISTS(SELECT 1 FROM scanned s LEFT JOIN heimdall_retention.event_identities i
 ON i.product_id=$7 AND i.environment=$8 AND i.event_id=s.event_id WHERE i.event_id IS NULL) AS missing_identity,
 coalesce((SELECT jsonb_agg(i.event_id ORDER BY i.event_id) FROM scanned s
 JOIN heimdall_retention.event_identities i ON i.product_id=$7 AND i.environment=$8 AND i.event_id=s.event_id
 WHERE i.tenant_id IS NOT DISTINCT FROM $11::text AND i.kind IN ('ordinary','request') AND i.recorded_at<=$12),'[]'::jsonb) AS selected
FROM (${retentionSQL}) admission`;

const retainedResolutionSQL = `SELECT snapshot.*,
 EXISTS(SELECT 1 FROM heimdall.audit_decisions WHERE product_id=$5 AND environment=$6 AND decision_id=$7)
 OR EXISTS(SELECT 1 FROM heimdall_retention.event_identities WHERE product_id=$5 AND environment=$6 AND decision_id=$7)
 OR EXISTS(SELECT 1 FROM heimdall_retention.decision_identities WHERE product_id=$5 AND environment=$6 AND decision_id=$7) AS source_present
 FROM (${retentionEligibilitySQL.replaceAll("$7", "(SELECT request_event_id FROM heimdall_retention.decision_identities WHERE product_id=$5 AND environment=$6 AND decision_id=$7)")}) snapshot`;

const holdDecisionSQL = `SELECT d.resolution,array_remove(ARRAY[d.request_event_id,d.terminal_event_id,d.recovery_event_id],NULL) AS ids FROM heimdall_retention.decision_identities d JOIN heimdall.audit_decisions s USING(product_id,environment,decision_id) JOIN heimdall_capacity.reservations r USING(product_id,environment,decision_id) WHERE d.product_id=$1 AND d.environment=$2 AND d.decision_id=$3 AND (s.request_event_id,s.requested_at,s.resolution,s.terminal_event_id,s.resolved_at) IS NOT DISTINCT FROM (d.request_event_id,d.requested_at,d.resolution,d.terminal_event_id,d.resolved_at) AND (r.request_event_id,r.resolution,r.terminal_event_id,r.recovery_event_id) IS NOT DISTINCT FROM (d.request_event_id,d.resolution,d.terminal_event_id,d.recovery_event_id)`;
