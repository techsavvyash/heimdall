import type { Pool } from "pg";
import { createHash } from "node:crypto";
import { validateEndpoint } from "../endpoints.js";
import { encodeIdentity } from "../identity-encoding.js";
import { HeimdallError } from "../errors.js";
import { providerId } from "../../provisioning/provider-request.js";
import { query, transaction } from "./query.js";
import { lockRetentionAdmission, retentionArgs, retentionSQL } from "./readiness.js";
import type { Heimdall, Principal } from "../../runtime.js";

function validRevision(value: string): boolean {
  return (
    typeof value === "string" &&
    /^[1-9][0-9]{0,18}(?![\s\S])/.test(value) &&
    BigInt(value) <= 9223372036854775807n
  );
}

export function createDirectoryCleanup(
  pool: Pool,
  options: {
    productId: string;
    environment: string;
    issuer: string;
    organizationId: string;
    transportProfile?: "production" | "loopback";
  },
) {
  const { productId, environment, issuer, organizationId } = options;
  if (
    !pool ||
    typeof pool.connect !== "function" ||
    ![productId, environment].every((id) => providerId(id) && id.length <= 64) ||
    !providerId(organizationId) ||
    typeof issuer !== "string"
  )
    throw new HeimdallError("invalid_argument");
  validateEndpoint(issuer, options.transportProfile ?? "production");
  if (new URL(issuer).pathname !== "/") throw new HeimdallError("invalid_argument");
  const scope = [productId, environment, issuer, organizationId];
  const cleanup = {
    async pending(after: string | null, limit: number, signal: AbortSignal) {
      if (
        !(signal instanceof AbortSignal) ||
        (after !== null && !validRevision(after)) ||
        !Number.isInteger(limit) ||
        limit < 1 ||
        limit > 100
      )
        throw new HeimdallError("invalid_argument");
      const rows = await query<{
        ready: boolean;
        subject: string | null;
        revision: string | null;
        observedAt: string | null;
        reason: "inactive" | "deleted" | null;
      }>(
        pool,
        `SELECT readiness.ready, d.subject, d.revision::text, d.observed_at::text AS "observedAt", d.reason
      FROM (${retentionSQL}) readiness LEFT JOIN LATERAL (
        SELECT subject,revision,observed_at,reason FROM heimdall_directory.deactivations
        WHERE readiness.ready AND product_id=$7 AND environment=$8 AND issuer=$9 AND organization_id=$10
          AND NOT completed AND revision>$11::bigint ORDER BY revision LIMIT $12
      ) d ON true ORDER BY d.revision`,
        [...retentionArgs, ...scope, after ?? "0", limit],
        false,
        signal,
      );
      if (!rows.length || rows.some((row) => row.ready !== true))
        throw new HeimdallError("unavailable");
      return rows.flatMap((row) =>
        row.subject === null
          ? []
          : [
              {
                subject: row.subject,
                revision: row.revision!,
                observedAt: Number(row.observedAt),
                reason: row.reason!,
              },
            ],
      );
    },
    /** Acknowledge only after the audited, recoverable membership operations are confirmed. */
    async complete(subject: string, revision: string, signal: AbortSignal) {
      if (!(signal instanceof AbortSignal) || !providerId(subject) || !validRevision(revision))
        throw new HeimdallError("invalid_argument");
      await transaction(
        pool,
        async (client) => {
          await lockRetentionAdmission(client);
          const identity = createHash("sha256").update(encodeIdentity(issuer, subject)).digest();
          const result = await client.query(
            `UPDATE heimdall_directory.deactivations SET completed=true
           WHERE product_id=$1 AND environment=$2 AND issuer=$3 AND organization_id=$4
             AND identity_hash=$5 AND subject=$6 AND revision=$7::bigint`,
            [...scope, identity, subject, revision],
          );
          if (result.rowCount !== 1) throw new HeimdallError("conflict");
        },
        signal,
      );
    },
  };
  return Object.freeze({
    ...cleanup,
    /** Apply the product's durable workspace plan through the managed membership API. */
    async reconcile(
      runtime: Pick<Heimdall, "tenant">,
      actor: Principal,
      input: {
        subject: string;
        revision: string;
        workspaces: readonly string[];
      },
      signal: AbortSignal,
    ) {
      const { subject, revision } = input;
      if (
        !(signal instanceof AbortSignal) ||
        !providerId(subject) ||
        !validRevision(revision) ||
        !Array.isArray(input.workspaces) ||
        input.workspaces.length > 100 ||
        !input.workspaces.every(providerId) ||
        new Set(input.workspaces).size !== input.workspaces.length
      )
        throw new HeimdallError("invalid_argument");
      if (
        !actor ||
        actor.productId !== productId ||
        actor.environment !== environment ||
        actor.issuer !== issuer
      )
        throw new HeimdallError("forbidden");
      const workspaces = [...input.workspaces].sort();
      const identity = createHash("sha256").update(encodeIdentity(issuer, subject)).digest();
      const plan = JSON.stringify([encodeIdentity(issuer, actor.subject), ...workspaces]);
      const completed = await transaction(
        pool,
        async (client) => {
          await lockRetentionAdmission(client);
          const result = await client.query<{ completed: boolean }>(
            `UPDATE heimdall_directory.deactivations SET cleanup_plan=$7::jsonb
           WHERE product_id=$1 AND environment=$2 AND issuer=$3 AND organization_id=$4
             AND identity_hash=$5 AND revision=$6::bigint
             AND (cleanup_plan=$7::jsonb OR (cleanup_plan IS NULL AND NOT completed))
             AND EXISTS(SELECT 1 FROM heimdall.identity_denials
               WHERE product_id=$1 AND environment=$2 AND identity_hash=$5)
           RETURNING completed`,
            [...scope, identity, revision, plan],
          );
          if (result.rowCount !== 1) throw new HeimdallError("conflict");
          return result.rows[0]!.completed;
        },
        signal,
      );
      if (completed) return { status: "confirmed" as const };
      for (const workspace of workspaces) {
        const memberships = runtime.tenant(workspace).memberships;
        if (!memberships) throw new HeimdallError("unavailable");
        const result = await memberships.remove(
          actor,
          {
            target: { issuer, subject },
            idempotencyKey: `directory-cleanup:${revision}:${workspace}`,
          },
          { signal },
        );
        if (result.status !== "confirmed") return result;
      }
      await cleanup.complete(subject, revision, signal);
      return { status: "confirmed" as const };
    },
  });
}
