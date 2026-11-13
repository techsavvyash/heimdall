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
import { validReactivationRevision } from "./directory-reactivation.js";

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
    input: {
      subject: string;
      workspaceId: string;
      ruleId: string;
      roles: readonly string[];
      expectedDirectRoles?: readonly string[];
      reactivationRevision?: string;
    },
    signal: AbortSignal,
  ) {
    const { subject, workspaceId, ruleId } = input;
    const revision = input.reactivationRevision;
    const validRoles = (values: readonly string[]) =>
      Array.isArray(values) &&
      values.length <= 128 &&
      values.every(
        (role) => typeof role === "string" && /^[a-z][a-z0-9_]{0,63}(?![\s\S])/.test(role),
      ) &&
      new Set(values).size === values.length;
    if (
      !(signal instanceof AbortSignal) ||
      ![subject, workspaceId, ruleId].every(providerId) ||
      !validRoles(input.roles) ||
      (revision !== undefined && !validReactivationRevision(revision)) ||
      (input.expectedDirectRoles !== undefined && !validRoles(input.expectedDirectRoles))
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
    const expected =
      input.expectedDirectRoles === undefined ? undefined : [...input.expectedDirectRoles].sort();
    const desired = [...new Set([...(expected ?? []), ...roles])].sort();
    if (desired.length > 128) throw new HeimdallError("invalid_argument");
    const identity = encodeIdentity(issuer, subject);
    const hash = createHash("sha256").update(identity).digest();
    const key = [productId, environment, issuer, organizationId, subject, workspaceId, ruleId];
    const actorKey = encodeIdentity(issuer, actor.subject);
    const plan = JSON.stringify(
      expected === undefined ? [actorKey, ...roles] : [actorKey, roles, expected],
    );
    const idempotencyKey =
      "directory-enroll:" +
      createHash("sha256")
        .update(JSON.stringify(revision === undefined ? key : [...key, revision]))
        .digest("hex");
    const observed = await reader.read(subject, signal);
    if (observed.state !== "active" || observed.externalId === undefined)
      throw new HeimdallError("forbidden");

    await transaction(
      pool,
      async (client) => {
        await lockRetentionAdmission(client);
        await client.query("SELECT pg_advisory_xact_lock($1::bigint)", [records.lock(hash)]);
        const result = await client.query(
          `INSERT INTO heimdall_directory.enrollments(product_id,environment,issuer,organization_id,subject,workspace_id,rule_id,plan,reactivation_revision)
        SELECT $1,$2,$3,$4,$5,$6,$7,$8::jsonb,$10::bigint
        WHERE ($10::bigint=0 AND NOT EXISTS (
          SELECT 1 FROM heimdall.identity_denials WHERE product_id=$1 AND environment=$2 AND identity_hash=$9))
        OR EXISTS (
          SELECT 1 FROM heimdall_directory.deactivations d JOIN heimdall.identity_denials n USING(product_id,environment,identity_hash)
          WHERE d.product_id=$1 AND d.environment=$2 AND d.issuer=$3 AND d.organization_id=$4 AND d.identity_hash=$9
            AND d.revision=$10::bigint AND d.completed AND NOT d.reactivation_released
            AND n.reason='inactive' AND n.observed_at<d.reactivation_started_at)
        ON CONFLICT(product_id,environment,issuer,organization_id,subject,workspace_id,rule_id,reactivation_revision)
        DO UPDATE SET plan=EXCLUDED.plan WHERE enrollments.plan=EXCLUDED.plan`,
          [...key, plan, hash, revision ?? "0"],
        );
        if (result.rowCount !== 1) throw new HeimdallError("conflict");
      },
      signal,
    );

    const mutation = { target: { issuer, subject }, roles: desired, idempotencyKey };
    const result =
      expected === undefined
        ? await memberships.add(actor, mutation, { signal })
        : await memberships.assignRoles(
            actor,
            { ...mutation, expectedDirectRoles: expected },
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
            AND subject=$5 AND workspace_id=$6 AND rule_id=$7 AND (operation_id IS NULL OR operation_id=$8)
            AND reactivation_revision=$9::bigint`,
            [...key, result.operationId, revision ?? "0"],
          );
          if (update.rowCount !== 1) throw new HeimdallError("conflict");
        },
        signal,
      );
    }
    return result;
  };
}
