import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { recoverWorkspaceCreation } from "../../dist/provisioning/index.js";

const fixture = JSON.parse(
  readFileSync(
    new URL("../../../../contracts/fixtures/tenant-model-v1.json", import.meta.url),
    "utf8",
  ),
).find(({ name }) => name === "ledger");

export function recoveryProbeConfig(environment, storeId, modelId) {
  const manifest = structuredClone(fixture.manifest);
  manifest.clients.web = {
    redirectUris: ["http://127.0.0.1:9090/callback"],
    transportProfile: "loopback",
  };
  const config = {
    schemaVersion: 1,
    productId: manifest.productId,
    environment,
    issuer: "http://127.0.0.1:9090",
    audience: "probe",
    clientId: "probe",
    zitadelProjectId: "probe",
    clientKey: "web",
    transportProfile: "loopback",
    manifestVersion: manifest.version,
    openfga: { url: "http://127.0.0.1:8080", storeId, modelId },
  };
  return { config, manifest };
}

export const recoveryModel = fixture.expected.model;
export const recoveryOwnerRelation = fixture.expected.roles[fixture.manifest.ownerRole].assignment;

export async function recoverTypeScript(dsn, input, recoveryToken) {
  const pool = new Pool({ connectionString: dsn });
  try {
    const { config, manifest, operationId, generation, evidenceId } = input;
    return await recoverWorkspaceCreation(
      pool,
      JSON.stringify(config),
      JSON.stringify(manifest),
      {
        operationId,
        expectedGeneration: generation,
        initiatingActor: { issuer: config.issuer, subject: "recovery-probe" },
        policyVersion: "probe",
        auditSinks: ["siem"],
        recoveryToken,
        verifyQuiescence: async (scope) => {
          assert.equal(scope.generation, generation);
          assert.equal(scope.operationId, operationId);
          assert.equal(scope.workspaceId, operationId);
          assert.equal(scope.storeId, config.openfga.storeId);
          assert.equal(scope.modelId, config.openfga.modelId);
          return { operatorId: "probe", evidenceId };
        },
      },
      AbortSignal.timeout(30000),
    );
  } finally {
    await pool.end();
  }
}
