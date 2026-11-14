import { createHash, randomBytes, randomUUID } from "node:crypto";
import { HeimdallError } from "@heimdall/sdk";
import { createCustomAuditWriter } from "@heimdall/sdk/postgres";
import { prepareEnterpriseProvider } from "@heimdall/sdk/provisioning";

const eventType = "product.sso.policy_published";
const identifier = /^[A-Za-z0-9_-]{1,128}$/;
const versionPattern = /^[a-f0-9]{32}$/;
const projection =
  'organization_id AS "organizationId", provider_id AS "providerId", version AS "policyVersion", protocol, 900000 AS "maxAgeMs"';
export const providerSettingsRoute =
  /^\/workspaces\/([A-Za-z0-9_-]{1,128})\/enterprise\/provider\/(prepare|test|activate)$/;
const fail = (code) => {
  throw new HeimdallError(code);
};

export function providerSettings(db, config, fallback, allowSettings = true) {
  const raw = allowSettings ? process.env.HEIMDALL_PROVIDER_SETTINGS : undefined;
  const decoded = raw ? JSON.parse(raw) : {};
  if (!decoded || typeof decoded !== "object" || Array.isArray(decoded)) fail("invalid_argument");
  const entries = Object.entries(decoded);
  if (
    (raw && entries.length === 0) ||
    entries.some(
      ([id, value]) =>
        !identifier.test(id) ||
        id.startsWith("sso-test-") ||
        typeof value?.organizationId !== "string" ||
        !identifier.test(value.organizationId) ||
        typeof value.testSubject !== "string" ||
        !identifier.test(value.testSubject),
    )
  )
    fail("invalid_argument");
  const mapping = new Map(entries);
  const token = process.env.HEIMDALL_PROVIDER_SETTINGS_TOKEN;
  if (raw && !token) fail("invalid_argument");
  const environment = config.environment;
  const active = async (workspace) =>
    (
      await db.query(
        `SELECT ${projection} FROM ledger.sso_publications p JOIN ledger.sso_candidates c
         USING (environment, workspace_id, version)
         WHERE environment=$1 AND workspace_id=$2 ORDER BY revision DESC LIMIT 1`,
        [environment, workspace],
      )
    ).rows[0];

  return {
    enabled: Boolean(raw),
    async policy(workspace, signal) {
      if (workspace.startsWith("sso-test-")) {
        if (!raw) return null;
        const row = (
          await db.query(
            `SELECT ${projection} FROM ledger.sso_candidates c
             WHERE environment=$1 AND verification_workspace=$2 AND provider_id IS NOT NULL
             AND NOT EXISTS (SELECT 1 FROM ledger.sso_publications p
               WHERE (p.environment,p.workspace_id,p.version)=(c.environment,c.workspace_id,c.version))`,
            [environment, workspace],
          )
        ).rows[0];
        return row ?? null;
      }
      return (await active(workspace)) ?? (await fallback?.(workspace, signal)) ?? null;
    },

    async handle(request, workspace, action, auth, sessions, ownerRole, loadInput) {
      const scope = mapping.get(workspace);
      if (!scope) fail("forbidden");
      const actor = await sessions.authenticate(request);
      const signal = request.signal;
      if (action === "test") {
        if (actor.subject !== scope.testSubject) fail("forbidden");
      }
      const activeVersion =
        action === "activate"
          ? ((await this.policy(workspace, signal))?.policyVersion ?? null)
          : null;
      if (
        action !== "test" &&
        !(await auth.tenant(workspace).roles(actor, { signal })).includes(ownerRole)
      )
        fail("forbidden");
      const input = await loadInput();
      const version = input.version;
      if (typeof version !== "string" || !versionPattern.test(version)) fail("invalid_argument");
      const key = [environment, workspace, version];
      if (action === "prepare") {
        const { protocol, issuer, clientId, clientSecret, metadataXml, existingProviderId } = input;
        const allowed =
          protocol === "oidc"
            ? ["version", "protocol", "issuer", "clientId", "clientSecret", "existingProviderId"]
            : ["version", "protocol", "metadataXml", "existingProviderId"];
        if (
          Object.keys(input).some((field) => !allowed.includes(field)) ||
          (existingProviderId !== undefined &&
            (typeof existingProviderId !== "string" ||
              !identifier.test(existingProviderId) ||
              clientSecret !== undefined))
        )
          fail("invalid_argument");
        let provider, material;
        if (protocol === "oidc" && typeof issuer === "string" && typeof clientId === "string") {
          provider = { protocol, issuer, clientId, clientSecret };
          material = [issuer, clientId];
        } else if (
          protocol === "saml" &&
          typeof metadataXml === "string" &&
          Buffer.from(metadataXml, "base64").toString("base64") === metadataXml
        ) {
          provider = { protocol, metadataXml: Buffer.from(metadataXml, "base64") };
          material = [metadataXml];
        } else fail("invalid_argument");
        const fingerprint = createHash("sha256").update(JSON.stringify(material)).digest("hex");
        if (existingProviderId === undefined) {
          await db.query(
            `INSERT INTO ledger.sso_candidates
             (environment, workspace_id, version, verification_workspace, organization_id, protocol, settings_digest)
             VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (environment,workspace_id,version) DO NOTHING`,
            [
              ...key,
              "sso-test-" + randomBytes(24).toString("hex"),
              scope.organizationId,
              protocol,
              fingerprint,
            ],
          );
        }
        const stored = (
          await db.query(
            `SELECT verification_workspace, dispatched, provider_id FROM ledger.sso_candidates
             WHERE environment=$1 AND workspace_id=$2 AND version=$3
               AND organization_id=$4 AND protocol=$5 AND settings_digest=$6`,
            [...key, scope.organizationId, protocol, fingerprint],
          )
        ).rows[0];
        if (
          !stored ||
          (existingProviderId !== undefined &&
            (!stored.dispatched ||
              (stored.provider_id && stored.provider_id !== existingProviderId)))
        )
          fail("conflict");
        const result = await prepareEnterpriseProvider(
          {
            issuer: config.issuer,
            organizationId: scope.organizationId,
            token,
            name: `heimdall-${config.productId}-${version}`,
            provider,
            ...(existingProviderId !== undefined
              ? { existingProviderId }
              : {
                  async claimDispatch() {
                    const claim = await db.query(
                      `UPDATE ledger.sso_candidates SET dispatched=true
                       WHERE environment=$1 AND workspace_id=$2 AND version=$3 AND NOT dispatched`,
                      key,
                    );
                    if (!claim.rowCount) fail("conflict");
                  },
                }),
          },
          signal,
        );
        const { rowCount } = await db.query(
          `UPDATE ledger.sso_candidates SET provider_id=$4
           WHERE environment=$1 AND workspace_id=$2 AND version=$3 AND dispatched
             AND (provider_id IS NULL OR provider_id=$4)`,
          [...key, result.providerId],
        );
        if (!rowCount) fail("conflict");
        return { ...result, version, verificationWorkspaceId: stored.verification_workspace };
      }

      if (action === "test") {
        if (Object.keys(input).length !== 1) fail("invalid_argument");
        const candidate = (
          await db.query(
            `SELECT verification_workspace, ${projection} FROM ledger.sso_candidates
             WHERE environment=$1 AND workspace_id=$2 AND version=$3 AND organization_id=$4
               AND provider_id IS NOT NULL`,
            [...key, scope.organizationId],
          )
        ).rows[0];
        if (!candidate) fail("conflict");
        await auth.verifyEnterpriseProviderTest(
          actor,
          candidate.verification_workspace,
          { ...candidate, maxAgeMs: 240_000 },
          { signal },
        );
        const { rowCount } = await db.query(
          `UPDATE ledger.sso_candidates c SET tested_subject=$4,tested_at=clock_timestamp()
           WHERE environment=$1 AND workspace_id=$2 AND version=$3
           AND NOT EXISTS (SELECT 1 FROM ledger.sso_publications p
             WHERE (p.environment,p.workspace_id,p.version)=(c.environment,c.workspace_id,c.version))`,
          [...key, actor.subject],
        );
        if (!rowCount) fail("conflict");
        return { tested: true };
      }

      const expected = input.expectedPolicyVersion;
      if (
        Object.keys(input).length !== 2 ||
        (expected !== null && (typeof expected !== "string" || !identifier.test(expected)))
      )
        fail("invalid_argument");
      if (activeVersion !== expected || activeVersion === version) fail("conflict");
      const baseline = (await fallback?.(workspace, signal))?.policyVersion ?? "";
      const id = randomUUID();
      const { rowCount } = await db.query(
        `INSERT INTO ledger.sso_publications
           (environment, workspace_id, version, previous_version, event_id, actor_issuer, actor_subject, recorded_at)
         SELECT $1,$2,$3,$4,$5,$6,$7,(extract(epoch FROM clock_timestamp())*1000)::bigint
           FROM ledger.sso_candidates WHERE environment=$1 AND workspace_id=$2 AND version=$3
             AND organization_id=$8 AND tested_subject=$9 AND tested_at > clock_timestamp()-interval '45 seconds'
             AND $4=coalesce((SELECT version FROM ledger.sso_publications
               WHERE environment=$1 AND workspace_id=$2 ORDER BY revision DESC LIMIT 1),$10)
         ON CONFLICT DO NOTHING`,
        [
          ...key,
          expected ?? "",
          id,
          actor.issuer,
          actor.subject,
          scope.organizationId,
          scope.testSubject,
          baseline,
        ],
      );
      if (!rowCount) fail("conflict");
      return { version, eventId: id };
    },
  };
}

export function validatePolicyPublication(type, version, attributes) {
  return (
    type === eventType &&
    version === 1 &&
    Object.keys(attributes).length === 2 &&
    typeof attributes.version === "string" &&
    versionPattern.test(attributes.version) &&
    (attributes.previousVersion === null ||
      (typeof attributes.previousVersion === "string" &&
        identifier.test(attributes.previousVersion)))
  );
}

export async function exportPolicyPublications(business, audit, scope, sinkId, tenantId, signal) {
  if (!tenantId) fail("invalid_argument");
  const writer = createCustomAuditWriter(audit, {
    ...scope,
    auditSinks: [sinkId],
    validateCustom: validatePolicyPublication,
  });
  const pending = await business.query(
    `SELECT * FROM ledger.sso_publications WHERE environment=$1 AND workspace_id=$2 AND NOT exported
     ORDER BY revision LIMIT 20`,
    [scope.environment, tenantId],
  );
  for (const row of pending.rows) {
    await writer.record(
      {
        eventId: row.event_id,
        requestId: row.event_id,
        eventType,
        eventVersion: 1,
        occurredAt: Number(row.recorded_at),
        recordedAt: Number(row.recorded_at),
        tenantId,
        actor: { kind: "user", issuer: row.actor_issuer, subject: row.actor_subject },
        target: { type: "workspace", id: tenantId },
        action: "sso.policy.publish",
        outcome: "succeeded",
        attributes: { version: row.version, previousVersion: row.previous_version || null },
      },
      signal,
    );
    await business.query(
      "UPDATE ledger.sso_publications SET exported=true WHERE environment=$1 AND workspace_id=$2 AND event_id=$3",
      [scope.environment, tenantId, row.event_id],
    );
  }
}
