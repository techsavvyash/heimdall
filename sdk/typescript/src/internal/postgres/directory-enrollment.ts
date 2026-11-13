import type { Pool } from "pg";
import { createHash } from "node:crypto";
import {
  createDirectoryReader,
  type DirectoryReaderOptions,
} from "../../provisioning/directory-reader.js";
import { providerId } from "../../provisioning/provider-request.js";
import { HeimdallError } from "../errors.js";
import { encodeIdentity } from "../identity-encoding.js";
import type { Heimdall, Principal } from "../../runtime.js";
import { transaction } from "./query.js";
import { lockRetentionAdmission } from "./readiness.js";
import { IdentityRecords } from "./identity.js";

export function createDirectoryEnrollment(
  pool: Pool,
  options: DirectoryReaderOptions & { productId: string; environment: string },
) {
  const reader = createDirectoryReader(options);
  const { productId, environment, issuer, organizationId } = options;
  if (
    !pool ||
    typeof pool.connect !== "function" ||
    ![productId, environment].every((value) => providerId(value) && value.length <= 64)
  )
    throw new HeimdallError("invalid_argument");
  const records = IdentityRecords.retainedRecords(pool, productId, environment);
  return async function enroll(
    runtime: Pick<Heimdall, "tenant">,
    actor: Principal,
    input: { subject: string; workspaceId: string; ruleId: string; roles: readonly string[] },
    signal: AbortSignal,
  ) {
    const { subject, workspaceId, ruleId } = input;
    if (
      !(signal instanceof AbortSignal) ||
      ![subject, workspaceId, ruleId].every(providerId) ||
      !Array.isArray(input.roles) ||
      input.roles.length > 128 ||
      !input.roles.every(
        (role) => typeof role === "string" && /^[a-z][a-z0-9_]{0,63}(?![\s\S])/.test(role),
      ) ||
      new Set(input.roles).size !== input.roles.length
    )
      throw new HeimdallError("invalid_argument");
    if (
      !actor ||
      actor.issuer !== issuer ||
      actor.productId !== productId ||
      actor.environment !== environment
    )
      throw new HeimdallError("forbidden");
    const memberships = runtime.tenant(workspaceId).memberships;
    if (!memberships) throw new HeimdallError("unavailable");
    const roles = [...input.roles].sort();
    const identity = encodeIdentity(issuer, subject);
    const hash = createHash("sha256").update(identity).digest();
    const key = [productId, environment, issuer, organizationId, subject, workspaceId, ruleId];
    const plan = JSON.stringify([encodeIdentity(issuer, actor.subject), ...roles]);
    const idempotencyKey =
      "directory-enroll:" + createHash("sha256").update(JSON.stringify(key)).digest("hex");
    const observed = await reader.read(subject, signal);
    if (observed.state !== "active" || observed.externalId === undefined)
      throw new HeimdallError("forbidden");

    await transaction(
      pool,
      async (client) => {
        await lockRetentionAdmission(client);
        await client.query("SELECT pg_advisory_xact_lock($1::bigint)", [records.lock(hash)]);
        const result = await client.query(
          `INSERT INTO heimdall_directory.enrollments(product_id,environment,issuer,organization_id,subject,workspace_id,rule_id,plan)
        SELECT $1,$2,$3,$4,$5,$6,$7,$8::jsonb WHERE NOT EXISTS (
          SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$9)
        ON CONFLICT(product_id,environment,issuer,organization_id,subject,workspace_id,rule_id)
        DO UPDATE SET plan=EXCLUDED.plan WHERE enrollments.plan=EXCLUDED.plan`,
          [...key, plan, hash],
        );
        if (result.rowCount !== 1) throw new HeimdallError("conflict");
      },
      signal,
    );

    const result = await memberships.add(
      actor,
      { target: { issuer, subject }, roles, idempotencyKey },
      { signal },
    );
    if (result.status === "confirmed") {
      await transaction(
        pool,
        async (client) => {
          await lockRetentionAdmission(client);
          const update = await client.query(
            `UPDATE heimdall_directory.enrollments SET operation_id=$8
          WHERE product_id=$1 AND environment=$2 AND issuer=$3 AND organization_id=$4
            AND subject=$5 AND workspace_id=$6 AND rule_id=$7 AND (operation_id IS NULL OR operation_id=$8)`,
            [...key, result.operationId],
          );
          if (update.rowCount !== 1) throw new HeimdallError("conflict");
        },
        signal,
      );
    }
    return result;
  };
}
