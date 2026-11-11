import { createHash } from "node:crypto";
import type { Pool } from "pg";
import { HeimdallError, checkCancellation } from "../errors.js";
import { encodeIdentity } from "../identity-encoding.js";
import {
  parseIntent,
  parseBoundIntent,
  intentDigests,
  nextOperationState,
  type Intent,
} from "../operations.js";
import { validateAuditShape } from "../audit-shape.js";
import { validateAuditEvent } from "../audit.js";
import { PostgresAudit } from "./audit.js";
import { query } from "./query.js";
import { decodeWorkspace } from "./workspaces.js";

type Binding = {
  productId: string;
  environment: string;
  storeId: string;
  modelId: string;
  modelHash: string;
  manifestVersion: number;
};
type Allocation = {
  workspaceId: string;
  operationId: string;
  eventId: string;
  requestId: string;
  policyVersion: string;
  occurredAt: number;
  recordedAt: number;
};
type Identity = Intent["actor"];
export type ManagedOperation = {
  schemaVersion: 1;
  operationId: string;
  workspaceId: string;
  intent: Intent;
  fingerprint: string;
  keyDigest: string;
  state: string;
  dispatchCount: number;
  generation: number;
  nextAttemptAt: number;
  createdAt: number;
  updatedAt: number;
};
const token = (v: unknown, max: number): v is string =>
  typeof v === "string" && v.length > 0 && v.length <= max && !/[^A-Za-z0-9_-]/.test(v);
const hash = (v: Identity) => {
  if (!v || typeof v !== "object") throw new HeimdallError("invalid_argument");
  return createHash("sha256").update(encodeIdentity(v.issuer, v.subject), "ascii").digest("hex");
};
const hex = (v: unknown): v is string => typeof v === "string" && /^[a-f0-9]{64}(?![\s\S])/.test(v);
function integer(v: unknown, min: number): number {
  if (
    typeof v !== "string" ||
    !/^(0|[1-9][0-9]{0,15})(?![\s\S])/.test(v) ||
    !Number.isSafeInteger(Number(v)) ||
    Number(v) < min
  )
    throw new HeimdallError("unavailable");
  return Number(v);
}
// Validate physical row, pinned configuration and actor ownership before exposing it.
export function decodeOperation(
  value: Record<string, unknown>,
  b: Binding,
  actorHash: string,
): ManagedOperation {
  try {
    if (
      !token(value.operationId, 128) ||
      !hex(value.keyDigest) ||
      value.actorHash !== actorHash ||
      value.storeId !== b.storeId ||
      value.modelId !== b.modelId ||
      value.modelHash !== b.modelHash ||
      value.productId !== b.productId ||
      value.environment !== b.environment ||
      value.manifestVersion !== b.manifestVersion ||
      typeof value.payload !== "string"
    )
      throw new Error();
    const intent = parseBoundIntent(value.payload, {
      productId: b.productId,
      environment: b.environment,
      workspaceId: value.workspaceId as string,
      kind: value.kind as string,
      actorHash: value.actorHash as string,
      targetHash: value.targetHash as string,
      fingerprint: value.fingerprint as string,
      manifestVersion: b.manifestVersion,
    });
    const dispatchCount = integer(value.dispatchCount, 0),
      generation = integer(value.generation, 1),
      nextAttemptAt = integer(value.nextAttemptAt, 0),
      createdAt = integer(value.createdAt, 0),
      updatedAt = integer(value.updatedAt, 0);
    if (
      typeof value.state !== "string" ||
      ![
        "pending",
        "retryable",
        "dispatching",
        "confirmed",
        "uncertain",
        "completed",
        "cancelled",
      ].includes(value.state) ||
      createdAt > updatedAt ||
      (["dispatching", "confirmed", "uncertain", "completed"].includes(value.state) &&
        dispatchCount < 1) ||
      (value.state === "cancelled" && dispatchCount !== 0)
    )
      throw new Error();
    return {
      schemaVersion: 1,
      operationId: value.operationId,
      workspaceId: value.workspaceId as string,
      intent,
      fingerprint: value.fingerprint as string,
      keyDigest: value.keyDigest,
      state: value.state,
      dispatchCount,
      generation,
      nextAttemptAt,
      createdAt,
      updatedAt,
    };
  } catch {
    throw new HeimdallError("unavailable");
  }
}
const columns = `jsonb_build_object('productId',o.product_id,'environment',o.environment,'operationId',o.operation_id,'workspaceId',o.workspace_id,'kind',o.kind,
 'actorHash',encode(o.actor_hash,'hex'),'targetHash',encode(o.target_hash,'hex'),'keyDigest',encode(o.key_digest,'hex'),'fingerprint',encode(o.fingerprint,'hex'),
 'payload',convert_from(o.intent_payload,'UTF8'),'storeId',o.store_id,'modelId',o.model_id,'modelHash',encode(o.model_hash,'hex'),'manifestVersion',o.manifest_version,
 'state',o.state,'dispatchCount',o.dispatch_count::text,'generation',o.generation::text,'nextAttemptAt',o.next_attempt_at::text,'createdAt',o.created_at::text,'updatedAt',o.updated_at::text)`;
function select(byKey: boolean) {
  return `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
 CASE WHEN o.operation_id IS NULL THEN NULL ELSE ${columns} END AS record
 FROM (SELECT 1) seed LEFT JOIN heimdall_managed.operations o ON o.product_id=$1 AND o.environment=$2 AND o.actor_hash=$3 AND ${byKey ? "o.key_digest=$4" : "o.operation_id=$4"}`;
}
const validateAudit = (e: Record<string, unknown>) => {
  validateAuditShape(e);
  validateAuditEvent(e as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
};
// Internal persistence: trusted SDK orchestration must authenticate and authorize first.
export class OperationRecords {
  private readonly binding: Readonly<Binding>;
  private readonly audit: PostgresAudit;
  constructor(
    private readonly pool: Pool,
    binding: Binding,
    sinks: readonly string[],
  ) {
    if (
      !pool ||
      typeof pool.connect !== "function" ||
      !binding ||
      !token(binding.productId, 64) ||
      !token(binding.environment, 64) ||
      !token(binding.storeId, 128) ||
      !token(binding.modelId, 128) ||
      !hex(binding.modelHash) ||
      !Number.isInteger(binding.manifestVersion) ||
      binding.manifestVersion < 1 ||
      binding.manifestVersion > 2147483647
    )
      throw new HeimdallError("invalid_argument");
    this.binding = Object.freeze({ ...binding });
    this.audit = new PostgresAudit(
      pool,
      binding.productId,
      binding.environment,
      sinks,
      validateAudit,
    );
  }
  private decode(
    rows: { primary: boolean; record: Record<string, unknown> | null }[],
    actorHash: string,
    selector: string,
    byKey: boolean,
  ) {
    if (rows.length !== 1 || rows[0]?.primary !== true) throw new HeimdallError("unavailable");
    if (rows[0].record === null) return null;
    const record = decodeOperation(rows[0].record, this.binding, actorHash);
    if ((byKey ? record.keyDigest : record.operationId) !== selector)
      throw new HeimdallError("unavailable");
    return record;
  }
  private async read(selector: string, actorHash: string, byKey: boolean, signal?: AbortSignal) {
    const b = this.binding;
    return this.decode(
      await query<{ primary: boolean; record: Record<string, unknown> | null }>(
        this.pool,
        select(byKey),
        [
          b.productId,
          b.environment,
          Buffer.from(actorHash, "hex"),
          byKey ? Buffer.from(selector, "hex") : selector,
        ],
        false,
        signal,
      ),
      actorHash,
      selector,
      byKey,
    );
  }
  // Trusted maintenance only: selection does not claim work or authorize remote replay.
  async scan(
    queue: "runnable" | "recovery" | "completion",
    after: string | null,
    now: number,
    limit: number,
    signal?: AbortSignal,
  ) {
    const states = {
      runnable: ["pending", "retryable"],
      recovery: ["dispatching", "uncertain"],
      completion: ["confirmed"],
    };
    if (
      !Object.hasOwn(states, queue) ||
      (after !== null && !token(after, 128)) ||
      !Number.isSafeInteger(now) ||
      now < 0 ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100
    )
      throw new HeimdallError("invalid_argument");
    const b = this.binding;
    const rows = await query<{
      primary: boolean;
      records: Record<string, unknown>[];
      selected_count: number;
    }>(
      this.pool,
      `
      WITH selected AS MATERIALIZED (
        SELECT * FROM heimdall_managed.operations WHERE product_id=$1 AND environment=$2 AND state=ANY($3::text[])
         AND ($4::text IS NULL OR operation_id COLLATE "C">$4 COLLATE "C") AND (NOT $5::boolean OR next_attempt_at<=$6)
         ORDER BY operation_id COLLATE "C" LIMIT $7
      ), encoded AS MATERIALIZED (
        SELECT o.operation_id, ${columns} AS record FROM selected o
      ), sized AS (
        SELECT *,sum(octet_length(record::text)) OVER(ORDER BY operation_id COLLATE "C") AS size FROM encoded
      ) SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
        COALESCE(jsonb_agg(o.record ORDER BY o.operation_id COLLATE "C"),'[]'::jsonb) AS records,
        (SELECT count(*)::int FROM selected) AS selected_count FROM sized o WHERE size<=4194304`,
      [b.productId, b.environment, states[queue], after, queue === "runnable", now, limit + 1],
      false,
      signal,
    );
    const row = rows[0];
    if (
      rows.length !== 1 ||
      row?.primary !== true ||
      !Array.isArray(row.records) ||
      !Number.isInteger(row.selected_count) ||
      row.selected_count < row.records.length ||
      row.selected_count > limit + 1 ||
      (row.selected_count > 0 && row.records.length === 0)
    )
      throw new HeimdallError("unavailable");
    let previous = after ?? "";
    const items = row.records.map((r) => {
      const record = decodeOperation(r, b, r.actorHash as string);
      if (
        record.operationId <= previous ||
        !states[queue].includes(record.state) ||
        (queue === "runnable" && record.nextAttemptAt > now)
      )
        throw new HeimdallError("unavailable");
      previous = record.operationId;
      return record;
    });
    const more = row.selected_count > limit || row.selected_count > items.length;
    if (items.length > limit) items.pop();
    return { items, nextOperationId: more ? items[items.length - 1]!.operationId : null };
  }
  async get(
    operationId: string,
    actor: Identity,
    signal?: AbortSignal,
  ): Promise<ManagedOperation | null> {
    if (!token(operationId, 128)) throw new HeimdallError("invalid_argument");
    return this.read(operationId, hash(actor), false, signal);
  }
  // Trusted orchestration supplies definitive outcome evidence; this port never calls OpenFGA.
  async advance(
    operationId: string,
    actor: Identity,
    expectedGeneration: number,
    transition: string,
    metadata: Omit<Allocation, "workspaceId" | "operationId"> & { nextAttemptAt: number },
    caller?: AbortSignal,
  ): Promise<ManagedOperation> {
    const signal = caller
      ? AbortSignal.any([caller, AbortSignal.timeout(5000)])
      : AbortSignal.timeout(5000);
    checkCancellation(signal);
    const m = { ...metadata };
    if (
      !token(operationId, 128) ||
      !Number.isSafeInteger(expectedGeneration) ||
      expectedGeneration < 1 ||
      !Number.isSafeInteger(m.nextAttemptAt) ||
      m.nextAttemptAt < 0 ||
      !Number.isSafeInteger(m.occurredAt) ||
      m.occurredAt < 0 ||
      !Number.isSafeInteger(m.recordedAt) ||
      m.recordedAt < m.occurredAt ||
      ![
        "dispatch",
        "pre_dispatch_failure",
        "retry",
        "confirm",
        "uncertain",
        "rejected",
        "complete",
        "cancel",
      ].includes(transition)
    )
      throw new HeimdallError("invalid_argument");
    const actorHash = hash(actor),
      b = this.binding;
    const before = await this.read(operationId, actorHash, false, signal);
    if (!before) throw new HeimdallError("conflict");
    const state = nextOperationState(
      before.state,
      transition,
      before.dispatchCount,
      before.generation,
      expectedGeneration,
    );
    if (
      before.generation === Number.MAX_SAFE_INTEGER ||
      (transition === "dispatch" && before.dispatchCount === Number.MAX_SAFE_INTEGER)
    )
      throw new HeimdallError("unavailable");
    const count = before.dispatchCount + (transition === "dispatch" ? 1 : 0);
    const family = before.intent.kind === "transfer_owner" ? "ownership" : "membership";
    const stage =
      transition === "dispatch" ? "attempt" : transition === "retry" ? "intent" : "completed";
    const outcome =
      transition === "dispatch"
        ? "attempted"
        : transition === "retry"
          ? "requested"
          : transition === "uncertain"
            ? "outcome_unknown"
            : ["confirm", "complete"].includes(transition)
              ? "succeeded"
              : "failed";
    const event = {
      schemaVersion: 1,
      eventId: m.eventId,
      eventType: `heimdall.${family}.${stage}`,
      eventVersion: 1,
      occurredAt: m.occurredAt,
      recordedAt: m.recordedAt,
      productId: b.productId,
      environment: b.environment,
      tenantId: before.workspaceId,
      actor: { kind: "user", ...before.intent.actor },
      target: { type: "workspace", id: before.workspaceId },
      action: `${before.intent.kind}.${transition}`,
      outcome,
      requestId: m.requestId,
      source: "sdk",
      sdkVersion: "0.0.0",
      attributes: {},
      operationId,
      modelId: b.modelId,
      manifestVersion: b.manifestVersion,
      policyVersion: m.policyVersion,
    };
    validateAudit(event);
    if (
      m.recordedAt < before.updatedAt ||
      m.nextAttemptAt < m.recordedAt ||
      (["dispatch", "retry"].includes(transition) && m.recordedAt < before.nextAttemptAt)
    )
      throw new HeimdallError("conflict");
    let result: ManagedOperation | null = null;
    try {
      await this.audit.appendWith(
        event,
        async (tx) => {
          // Workspace-first lock order also serializes future administrative lifecycle changes.
          const rows = await tx.query(
            `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
          product_id,environment,workspace_id,name,lifecycle,mutation_blocked,revision::text,created_at::text,updated_at::text
          FROM heimdall_managed.workspaces WHERE product_id=$1 AND environment=$2 AND workspace_id=$3 FOR UPDATE`,
            [b.productId, b.environment, before.workspaceId],
          );
          if (rows.rows.length !== 1) throw new HeimdallError("unavailable");
          const w = decodeWorkspace(rows.rows[0], {
            productId: b.productId,
            environment: b.environment,
            workspaceId: before.workspaceId,
          });
          const creating = before.intent.kind === "create_workspace";
          if (
            !w ||
            !w.mutationBlocked ||
            m.recordedAt < w.updatedAt ||
            (creating
              ? w.lifecycle !== "pending"
              : !["active", "suspended"].includes(w.lifecycle)) ||
            (!creating && transition === "dispatch" && w.lifecycle !== "active")
          )
            throw new HeimdallError("conflict");
          const changed = await tx.query(
            `UPDATE heimdall_managed.operations SET state=$5,dispatch_count=$6,generation=generation+1,next_attempt_at=$7,updated_at=$8
          WHERE product_id=$1 AND environment=$2 AND operation_id=$3 AND generation=$4 AND state=$9 RETURNING operation_id`,
            [
              b.productId,
              b.environment,
              operationId,
              expectedGeneration,
              state,
              count,
              m.nextAttemptAt,
              m.recordedAt,
              before.state,
            ],
          );
          if (changed.rowCount !== 1) throw new HeimdallError("conflict");
          if (transition === "complete") {
            const targetHash = Buffer.from(hash(before.intent.target), "hex");
            if (before.intent.kind === "remove_member") {
              await tx.query(
                "DELETE FROM heimdall_managed.membership_candidates WHERE product_id=$1 AND environment=$2 AND workspace_id=$3 AND identity_hash=$4",
                [b.productId, b.environment, before.workspaceId, targetHash],
              );
            } else {
              const inserted = await tx.query(
                `INSERT INTO heimdall_managed.membership_candidates(product_id,environment,workspace_id,identity_hash,principal,observed_at)
              VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(product_id,environment,identity_hash,workspace_id)
              DO UPDATE SET observed_at=EXCLUDED.observed_at WHERE heimdall_managed.membership_candidates.principal=EXCLUDED.principal
              AND heimdall_managed.membership_candidates.observed_at<=EXCLUDED.observed_at RETURNING position`,
                [
                  b.productId,
                  b.environment,
                  before.workspaceId,
                  targetHash,
                  encodeIdentity(before.intent.target.issuer, before.intent.target.subject),
                  m.recordedAt,
                ],
              );
              if (inserted.rowCount !== 1) throw new HeimdallError("conflict");
            }
          }
          await tx.query(
            `UPDATE heimdall_managed.workspaces SET lifecycle=$4,mutation_blocked=$5,updated_at=$6
          WHERE product_id=$1 AND environment=$2 AND workspace_id=$3`,
            [
              b.productId,
              b.environment,
              before.workspaceId,
              creating && transition === "complete"
                ? "active"
                : creating && transition === "cancel"
                  ? "tombstoned"
                  : w.lifecycle,
              !["complete", "cancel"].includes(transition),
              m.recordedAt,
            ],
          );
          const after = await tx.query<{
            primary: boolean;
            record: Record<string, unknown> | null;
          }>(select(false), [
            b.productId,
            b.environment,
            Buffer.from(actorHash, "hex"),
            operationId,
          ]);
          result = this.decode(after.rows, actorHash, operationId, false);
          if (!result) throw new HeimdallError("unavailable");
        },
        signal,
      );
    } catch (e) {
      if (e instanceof HeimdallError && e.code === "outcome_unknown")
        throw new HeimdallError("outcome_unknown", operationId);
      throw e;
    }
    return result!;
  }
  create(
    intentJSON: string,
    key: string,
    allocation: Allocation,
    caller?: AbortSignal,
  ): Promise<ManagedOperation> {
    return this.begin(intentJSON, key, allocation, true, undefined, caller);
  }
  beginMembership(
    intentJSON: string,
    key: string,
    allocation: Allocation,
    expectedRevision: string,
    caller?: AbortSignal,
  ): Promise<ManagedOperation> {
    return this.begin(intentJSON, key, allocation, false, expectedRevision, caller);
  }
  private async begin(
    intentJSON: string,
    key: string,
    allocation: Allocation,
    creating: boolean,
    expectedRevision: string | undefined,
    caller?: AbortSignal,
  ): Promise<ManagedOperation> {
    const signal = caller
      ? AbortSignal.any([caller, AbortSignal.timeout(5000)])
      : AbortSignal.timeout(5000);
    checkCancellation(signal);
    const i = parseIntent(intentJSON),
      d = intentDigests(i, key),
      b = this.binding,
      a = { ...allocation };
    if (
      (creating
        ? i.kind !== "create_workspace"
        : i.kind === "create_workspace" ||
          i.workspaceId !== a.workspaceId ||
          typeof expectedRevision !== "string" ||
          !/^[1-9][0-9]{0,18}(?![\s\S])/.test(expectedRevision) ||
          BigInt(expectedRevision) > 9223372036854775807n) ||
      i.productId !== b.productId ||
      i.environment !== b.environment ||
      i.manifestVersion !== b.manifestVersion ||
      !token(a.workspaceId, 128) ||
      !token(a.operationId, 128)
    )
      throw new HeimdallError("invalid_argument");
    const event = {
      schemaVersion: 1,
      eventId: a.eventId,
      eventType:
        i.kind === "transfer_owner" ? "heimdall.ownership.intent" : "heimdall.membership.intent",
      eventVersion: 1,
      occurredAt: a.occurredAt,
      recordedAt: a.recordedAt,
      productId: b.productId,
      environment: b.environment,
      tenantId: a.workspaceId,
      actor: { kind: "user", ...i.actor },
      target: { type: "workspace", id: a.workspaceId },
      action: creating ? "workspace.create" : i.kind,
      outcome: "requested",
      requestId: a.requestId,
      source: "sdk",
      sdkVersion: "0.0.0",
      attributes: {},
      operationId: a.operationId,
      modelId: b.modelId,
      manifestVersion: b.manifestVersion,
      policyVersion: a.policyVersion,
    };
    validateAudit(event);
    const actorHash = hash(i.actor),
      targetHash = hash(i.target),
      payload = Buffer.from(JSON.stringify(i), "utf8");
    const existing = await this.read(d.keyDigest, actorHash, true, signal);
    if (existing) {
      if (existing.fingerprint !== d.fingerprint) throw new HeimdallError("conflict");
      return existing;
    }
    let result: ManagedOperation | null = null;
    try {
      await this.audit.appendWith(
        event,
        async (tx) => {
          if (creating) {
            await tx.query(
              "INSERT INTO heimdall_managed.workspaces(product_id,environment,workspace_id,name,lifecycle,mutation_blocked,created_at,updated_at) VALUES($1,$2,$3,$4,'pending',true,$5,$5)",
              [b.productId, b.environment, a.workspaceId, i.name, a.recordedAt],
            );
          } else {
            // Workspace-first CAS reserves the mutation slot without holding a network call.
            const reserved = await tx.query(
              `UPDATE heimdall_managed.workspaces SET mutation_blocked=true,updated_at=$5
              WHERE product_id=$1 AND environment=$2 AND workspace_id=$3 AND revision=$4::bigint
              AND lifecycle='active' AND NOT mutation_blocked AND updated_at<=$5
              RETURNING NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
               product_id,environment,workspace_id,name,lifecycle,mutation_blocked,revision::text,created_at::text,updated_at::text`,
              [b.productId, b.environment, a.workspaceId, expectedRevision, a.recordedAt],
            );
            if (reserved.rows.length !== 1) throw new HeimdallError("conflict");
            if (
              !decodeWorkspace(reserved.rows[0], {
                productId: b.productId,
                environment: b.environment,
                workspaceId: a.workspaceId,
              })
            )
              throw new HeimdallError("unavailable");
          }
          await tx.query(
            `INSERT INTO heimdall_managed.operations(product_id,environment,operation_id,workspace_id,kind,actor_hash,target_hash,key_digest,fingerprint,intent_payload,store_id,model_id,model_hash,manifest_version,state,dispatch_count,generation,next_attempt_at,created_at,updated_at)
      VALUES($1,$2,$3,$4,$15,$5,$6,$7,$8,$9,$10,$11,$12,$13,'pending',0,1,$14,$14,$14)`,
            [
              b.productId,
              b.environment,
              a.operationId,
              a.workspaceId,
              Buffer.from(actorHash, "hex"),
              Buffer.from(targetHash, "hex"),
              Buffer.from(d.keyDigest, "hex"),
              Buffer.from(d.fingerprint, "hex"),
              payload,
              b.storeId,
              b.modelId,
              Buffer.from(b.modelHash, "hex"),
              b.manifestVersion,
              a.recordedAt,
              i.kind,
            ],
          );
          const rows = await tx.query<{ primary: boolean; record: Record<string, unknown> | null }>(
            select(false),
            [b.productId, b.environment, Buffer.from(actorHash, "hex"), a.operationId],
          );
          result = this.decode(rows.rows, actorHash, a.operationId, false);
          if (!result) throw new HeimdallError("unavailable");
        },
        signal,
      );
    } catch (e) {
      if (e instanceof HeimdallError && e.code === "outcome_unknown")
        throw new HeimdallError("outcome_unknown", a.operationId);
      if (e instanceof HeimdallError && e.code === "conflict") {
        const won = await this.read(d.keyDigest, actorHash, true, signal);
        if (won && won.fingerprint === d.fingerprint) return won;
      }
      throw e;
    }
    return result!;
  }
}
