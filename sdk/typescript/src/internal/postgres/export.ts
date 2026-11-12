import { managedReadQuery } from "./readiness.js";
import type { Pool } from "pg";
import { HeimdallError } from "../errors.js";
import { query } from "./query.js";
type Kind = "workspaces" | "operations";
type Metadata = Record<string, string | number | boolean>;
const token = (v: unknown, max = 128): v is string =>
  typeof v === "string" && v.length > 0 && v.length <= max && !/[^A-Za-z0-9_-]/.test(v);
const common = ["productId", "environment", "workspaceId", "createdAt", "updatedAt"];
const workspace = ["lifecycle", "mutationBlocked", "revision"];
const operation = [
  "operationId",
  "kind",
  "state",
  "dispatchCount",
  "generation",
  "nextAttemptAt",
  "storeId",
  "modelId",
  "modelHash",
  "manifestVersion",
];
// Explicit allowlist: never reflect extra database fields into the export.
export function decodeExportRow(
  value: unknown,
  kind: Kind,
  product: string,
  environment: string,
  workspaceId: string | null,
): Metadata {
  const bad = (): never => {
    throw new HeimdallError("unavailable");
  };
  if (!value || typeof value !== "object" || Array.isArray(value)) return bad();
  const row = value as Record<string, unknown>;
  const keys = [...common, ...(kind === "workspaces" ? workspace : operation)];
  if (
    Object.keys(row).length !== keys.length ||
    keys.some((k) => !Object.hasOwn(row, k)) ||
    row.productId !== product ||
    row.environment !== environment ||
    !token(row.workspaceId) ||
    (workspaceId !== null && row.workspaceId !== workspaceId)
  )
    return bad();
  const out: Metadata = {};
  for (const key of keys) {
    const v = row[key];
    if (
      [
        "createdAt",
        "updatedAt",
        "dispatchCount",
        "generation",
        "nextAttemptAt",
        "manifestVersion",
      ].includes(key)
    ) {
      if (
        typeof v !== "string" ||
        !/^(0|[1-9][0-9]{0,15})(?![\s\S])/.test(v) ||
        !Number.isSafeInteger(Number(v)) ||
        (["generation", "manifestVersion"].includes(key) && Number(v) < 1) ||
        (key === "manifestVersion" && Number(v) > 2147483647)
      )
        return bad();
      out[key] = Number(v);
    } else if (key === "mutationBlocked") {
      if (typeof v !== "boolean") return bad();
      out[key] = v;
    } else {
      if (typeof v !== "string") return bad();
      out[key] = v;
    }
  }
  if (Number(out.createdAt) > Number(out.updatedAt)) return bad();
  if (kind === "workspaces") {
    if (
      !["pending", "active", "suspended", "tombstoned"].includes(String(out.lifecycle)) ||
      !/^[1-9][0-9]{0,18}(?![\s\S])/.test(String(out.revision)) ||
      BigInt(String(out.revision)) > 9223372036854775807n
    )
      return bad();
  } else {
    if (
      !["operationId", "storeId", "modelId"].every((k) => token(out[k])) ||
      !/^[a-f0-9]{64}(?![\s\S])/.test(String(out.modelHash)) ||
      ![
        "create_workspace",
        "add_member",
        "remove_member",
        "assign_roles",
        "transfer_owner",
      ].includes(String(out.kind)) ||
      ![
        "pending",
        "retryable",
        "dispatching",
        "confirmed",
        "uncertain",
        "completed",
        "cancelled",
      ].includes(String(out.state)) ||
      (["dispatching", "confirmed", "uncertain", "completed"].includes(String(out.state)) &&
        Number(out.dispatchCount) < 1) ||
      (out.state === "cancelled" && out.dispatchCount !== 0)
    )
      return bad();
  }
  return out;
}
// Privileged diagnostic export primitive; caller supplies authorization and durable export audit.
export class ManagedExport {
  private retainedRuntime = false;
  static forRetainedRuntime(pool: Pool, productId: string, environment: string) {
    const store = new ManagedExport(pool, productId, environment);
    store.retainedRuntime = true;
    return store;
  }

  // Legacy schema port; runtime exports use forRetainedRuntime.
  constructor(
    private readonly pool: Pool,
    private readonly productId: string,
    private readonly environment: string,
  ) {
    if (!pool || !token(productId, 64) || !token(environment, 64))
      throw new HeimdallError("invalid_argument");
  }
  async scan(
    kind: Kind,
    workspaceId: string | null,
    after: string | null,
    limit: number,
    signal?: AbortSignal,
  ): Promise<string> {
    if (
      !["workspaces", "operations"].includes(kind) ||
      (workspaceId !== null && !token(workspaceId)) ||
      (kind === "operations" && workspaceId === null) ||
      (after !== null && !token(after)) ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100
    )
      throw new HeimdallError("invalid_argument");
    const id = kind === "workspaces" ? "workspace_id" : "operation_id";
    const fields =
      kind === "workspaces"
        ? "'lifecycle',lifecycle,'mutationBlocked',mutation_blocked,'revision',revision::text"
        : "'operationId',operation_id,'kind',kind,'state',state,'dispatchCount',dispatch_count::text,'generation',generation::text,'nextAttemptAt',next_attempt_at::text,'storeId',store_id,'modelId',model_id,'modelHash',encode(model_hash,'hex'),'manifestVersion',manifest_version::text";
    const rows = await query<{ primary: boolean; records: unknown[] }>(
      this.pool,
      ...managedReadQuery(
        `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
 COALESCE(jsonb_agg(record ORDER BY id COLLATE "C"),'[]'::jsonb) AS records FROM (
 SELECT ${id} AS id,jsonb_build_object('productId',product_id,'environment',environment,'workspaceId',workspace_id,'createdAt',created_at::text,'updatedAt',updated_at::text,${fields}) AS record
 FROM heimdall_managed.${kind} WHERE product_id=$1 AND environment=$2 AND ($3::text IS NULL OR workspace_id=$3)
 AND ($4::text IS NULL OR ${id} COLLATE "C">$4 COLLATE "C") ORDER BY ${id} COLLATE "C" LIMIT $5) page`,
        [this.productId, this.environment, workspaceId, after, limit + 1],
        this.retainedRuntime,
      ),
      false,
      signal,
    );
    if (
      rows.length !== 1 ||
      rows[0]!.primary !== true ||
      !Array.isArray(rows[0]!.records) ||
      rows[0]!.records.length > limit + 1
    )
      throw new HeimdallError("unavailable");
    let previous = after ?? "";
    const items = rows[0]!.records.map((row) => {
      const item = decodeExportRow(row, kind, this.productId, this.environment, workspaceId);
      const id = String(kind === "workspaces" ? item.workspaceId : item.operationId);
      if (id <= previous) throw new HeimdallError("unavailable");
      previous = id;
      return item;
    });
    const more = items.length > limit;
    if (more) items.pop();
    return JSON.stringify({
      schemaVersion: 1,
      productId: this.productId,
      environment: this.environment,
      kind,
      workspaceId,
      items,
      nextId: more ? items.at(-1)![kind === "workspaces" ? "workspaceId" : "operationId"] : null,
    });
  }
}
