import { managedReadQuery } from "./readiness.js";
import type { Pool } from "pg";
import { HeimdallError } from "../errors.js";
import { query } from "./query.js";

type Scope = { productId: string; environment: string; workspaceId: string };
export type ManagedWorkspace = Scope & {
  schemaVersion: 1;
  name: string;
  lifecycle: "pending" | "active" | "suspended" | "tombstoned";
  mutationBlocked: boolean;
  revision: string;
  createdAt: number;
  updatedAt: number;
};
const token = (value: unknown, max: number): value is string =>
  typeof value === "string" &&
  value.length > 0 &&
  value.length <= max &&
  !/[^A-Za-z0-9_-]/.test(value);
const decimal = (v: unknown, max: bigint, positive: boolean): v is string =>
  typeof v === "string" &&
  /^(0|[1-9][0-9]{0,18})(?![\s\S])/.test(v) &&
  BigInt(v) <= max &&
  (!positive || v !== "0");
// Database rows are untrusted even when the installed SQL has constraints.
export function decodeWorkspace(
  row: Record<string, unknown>,
  scope: Scope,
): ManagedWorkspace | null {
  const bad = (): never => {
    throw new HeimdallError("unavailable");
  };
  if (row.primary !== true) return bad();
  if (row.workspace_id === null) {
    if (
      [
        "product_id",
        "environment",
        "name",
        "lifecycle",
        "mutation_blocked",
        "revision",
        "created_at",
        "updated_at",
      ].some((k) => row[k] !== null)
    )
      return bad();
    return null;
  }
  if (
    row.product_id !== scope.productId ||
    row.environment !== scope.environment ||
    row.workspace_id !== scope.workspaceId ||
    typeof row.name !== "string" ||
    !row.name.isWellFormed() ||
    Array.from(row.name).length < 1 ||
    Array.from(row.name).length > 256 ||
    Buffer.byteLength(row.name) > 1024 ||
    typeof row.lifecycle !== "string" ||
    !["pending", "active", "suspended", "tombstoned"].includes(row.lifecycle) ||
    typeof row.mutation_blocked !== "boolean" ||
    !decimal(row.revision, 9223372036854775807n, true) ||
    !decimal(row.created_at, 9007199254740991n, false) ||
    !decimal(row.updated_at, 9007199254740991n, false) ||
    BigInt(row.created_at) > BigInt(row.updated_at)
  )
    return bad();
  return {
    schemaVersion: 1,
    ...scope,
    name: row.name,
    lifecycle: row.lifecycle as ManagedWorkspace["lifecycle"],
    mutationBlocked: row.mutation_blocked,
    revision: row.revision,
    createdAt: Number(row.created_at),
    updatedAt: Number(row.updated_at),
  };
}
// Read-only primary port. No cache, migration, lifecycle mutation or authorization decision.
export class WorkspaceRecords {
  private retainedRuntime = false;
  static forRetainedRuntime(pool: Pool, productId: string, environment: string) {
    const store = new WorkspaceRecords(pool, productId, environment);
    store.retainedRuntime = true;
    return store;
  }

  // Legacy schema constructor; runtime callers use forRetainedRuntime.
  constructor(
    private readonly pool: Pool,
    private readonly productId: string,
    private readonly environment: string,
  ) {
    if (
      !pool ||
      typeof pool.connect !== "function" ||
      !token(productId, 64) ||
      !token(environment, 64)
    )
      throw new HeimdallError("invalid_argument");
  }
  async get(workspaceId: string, signal?: AbortSignal): Promise<ManagedWorkspace | null> {
    if (!token(workspaceId, 128)) throw new HeimdallError("invalid_argument");
    const rows = await query<Record<string, unknown>>(
      this.pool,
      ...managedReadQuery(
        `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
       w.product_id,w.environment,w.workspace_id,w.name,w.lifecycle,w.mutation_blocked,
       w.revision::text,w.created_at::text,w.updated_at::text
       FROM (SELECT 1) seed LEFT JOIN heimdall_managed.workspaces w
       ON w.product_id=$1 AND w.environment=$2 AND w.workspace_id=$3`,
        [this.productId, this.environment, workspaceId],
        this.retainedRuntime,
      ),
      false,
      signal,
    );
    if (rows.length !== 1) throw new HeimdallError("unavailable");
    return decodeWorkspace(rows[0]!, {
      productId: this.productId,
      environment: this.environment,
      workspaceId,
    });
  }
}
