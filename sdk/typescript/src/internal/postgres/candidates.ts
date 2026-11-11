import { createHash } from "node:crypto";
import type { Pool } from "pg";
import { HeimdallError } from "../errors.js";
import { encodeIdentity } from "../identity-encoding.js";
import { decodeWorkspace, type ManagedWorkspace } from "./workspaces.js";
import { query } from "./query.js";

type Scope = { productId: string; environment: string };
type Identity = { issuer: string; subject: string };
type Candidate = {
  position: string;
  revision: string;
  observedAt: number;
  workspace: ManagedWorkspace;
};
export type CandidatePage = { items: Candidate[]; nextPosition: string | null };
const token = (v: unknown, max: number): v is string =>
  typeof v === "string" && v.length > 0 && v.length <= max && !/[^A-Za-z0-9_-]/.test(v);
const decimal = (v: unknown, positive: boolean, max = 9223372036854775807n): v is string =>
  typeof v === "string" &&
  /^(0|[1-9][0-9]{0,18})(?![\s\S])/.test(v) &&
  BigInt(v) <= max &&
  (!positive || v !== "0");
// Internal source positions are not authenticated public cursors or authorization decisions.
export function decodeCandidatePage(
  value: { primary: boolean; records: unknown },
  scope: Scope,
  principal: string,
  identityHash: string,
  after: string,
  limit: number,
): CandidatePage {
  try {
    if (value.primary !== true || !Array.isArray(value.records) || value.records.length > limit + 1)
      throw new Error();
    let previous = BigInt(after);
    const seen = new Set<string>();
    const items: Candidate[] = value.records.map((r: Record<string, unknown>) => {
      if (
        !r ||
        r.identityHash !== identityHash ||
        r.principal !== principal ||
        !decimal(r.position, true) ||
        BigInt(r.position) <= previous ||
        !decimal(r.revision, true) ||
        !decimal(r.observedAt, false, 9007199254740991n) ||
        !r.workspace ||
        typeof r.workspace !== "object"
      )
        throw new Error();
      const row = r.workspace as Record<string, unknown>;
      if (!token(row.workspace_id, 128) || seen.has(row.workspace_id)) throw new Error();
      seen.add(row.workspace_id);
      const workspace = decodeWorkspace(row, { ...scope, workspaceId: row.workspace_id });
      if (!workspace || workspace.lifecycle !== "active" || workspace.mutationBlocked)
        throw new Error();
      previous = BigInt(r.position);
      return {
        position: r.position,
        revision: r.revision,
        observedAt: Number(r.observedAt),
        workspace,
      };
    });
    const more = items.length > limit;
    if (more) items.pop();
    return { items, nextPosition: more ? items[items.length - 1]!.position : null };
  } catch {
    throw new HeimdallError("unavailable");
  }
}
export class CandidateRecords {
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
  async scan(
    actor: Identity,
    after: string,
    limit: number,
    signal?: AbortSignal,
  ): Promise<CandidatePage> {
    if (
      !actor ||
      typeof actor !== "object" ||
      !decimal(after, false) ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100
    )
      throw new HeimdallError("invalid_argument");
    const principal = encodeIdentity(actor.issuer, actor.subject),
      identityHash = createHash("sha256").update(principal, "ascii").digest("hex");
    const rows = await query<{ primary: boolean; records: unknown }>(
      this.pool,
      `SELECT NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off' AS primary,
   COALESCE(jsonb_agg(jsonb_build_object('identityHash',encode(p.identity_hash,'hex'),'principal',p.principal,'position',p.position::text,
    'revision',p.candidate_revision::text,'observedAt',p.observed_at::text,'workspace',jsonb_build_object('primary',true,
    'product_id',p.product_id,'environment',p.environment,'workspace_id',p.workspace_id,'name',p.name,'lifecycle',p.lifecycle,
    'mutation_blocked',p.mutation_blocked,'revision',p.revision::text,'created_at',p.created_at::text,'updated_at',p.updated_at::text)) ORDER BY p.position),'[]'::jsonb) AS records
   FROM (SELECT c.identity_hash,c.principal,c.position,c.revision AS candidate_revision,c.observed_at,w.*
    FROM heimdall_managed.membership_candidates c JOIN heimdall_managed.workspaces w USING(product_id,environment,workspace_id)
    WHERE c.product_id=$1 AND c.environment=$2 AND c.identity_hash=$3 AND c.position>$4::bigint
     AND w.lifecycle='active' AND NOT w.mutation_blocked ORDER BY c.position LIMIT $5) p`,
      [this.productId, this.environment, Buffer.from(identityHash, "hex"), after, limit + 1],
      false,
      signal,
    );
    if (rows.length !== 1) throw new HeimdallError("unavailable");
    return decodeCandidatePage(
      rows[0]!,
      { productId: this.productId, environment: this.environment },
      principal,
      identityHash,
      after,
      limit,
    );
  }
}
