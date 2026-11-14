import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { parseDeployment } from "../src/internal/configuration.js";
import { setupProduct } from "../src/provisioning/index.js";

test.skipIf(
  !process.env.HEIMDALL_TEST_SETUP_DSN ||
    !process.env.HEIMDALL_TEST_SETUP_ZITADEL_ORG ||
    !process.env.HEIMDALL_PROBE_OPENFGA_KEY,
)("public setup emits validated runtime config for every manifest client", async () => {
  const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_SETUP_DSN });
  const { manifest } = JSON.parse(
    readFileSync(
      new URL("../../../contracts/fixtures/configuration-v1.json", import.meta.url),
      "utf8",
    ),
  );
  manifest.clients.admin = { redirectUris: ["https://ledger.example.invalid/admin/callback"] };
  const raw = JSON.stringify(manifest);
  const plan = {
    productId: "ledger",
    environment: "setup-public-ts",
    issuer: process.env.HEIMDALL_PROBE_ZITADEL_ISSUER!,
    organizationId: process.env.HEIMDALL_TEST_SETUP_ZITADEL_ORG!,
    projectName: "Ledger setup verification",
    openfgaUrl: process.env.HEIMDALL_PROBE_OPENFGA_URL!,
  };
  const saved = new Map<string, string>();
  const options = {
    zitadelToken: process.env.HEIMDALL_PROBE_ZITADEL_PAT!,
    openfgaToken: process.env.HEIMDALL_PROBE_OPENFGA_KEY!,
    async saveSecret(id: string, client: string, secret: string) {
      if (!secret || saved.has(id)) throw new Error("invalid secret handoff");
      saved.set(id, client);
    },
    async hasSecret(id: string, client: string) {
      return saved.get(id) === client;
    },
  };
  let store: string | undefined;
  async function cleanup() {
    if (!store) return;
    const response = await fetch(plan.openfgaUrl + "/stores/" + store, {
      method: "DELETE",
      redirect: "error",
      signal: AbortSignal.timeout(5000),
      headers: { authorization: "Bearer " + options.openfgaToken },
    });
    if (!response.ok) throw new Error("disposable store cleanup failed");
  }
  try {
    const signal = AbortSignal.timeout(25_000);
    const configs = await setupProduct(pool, plan, raw, options, signal);
    store = configs.web!.openfga.storeId;
    expect(Object.keys(configs)).toEqual(["admin", "web"]);
    expect(configs.web!.clientId).not.toBe(configs.admin!.clientId);
    for (const config of Object.values(configs)) {
      expect(parseDeployment(JSON.stringify(config), raw).config).toEqual(config);
      expect(config.audience).toBe(config.zitadelProjectId);
    }
    expect(await setupProduct(pool, plan, raw, options, signal)).toEqual(configs);
    expect(saved.size).toBe(2);
    await expect(
      setupProduct(pool, { ...plan, projectName: "Drift" }, raw, options, signal),
    ).rejects.toThrow("conflict");
  } finally {
    try {
      await cleanup();
    } finally {
      await pool.end();
    }
  }
});
