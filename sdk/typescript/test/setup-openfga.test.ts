import { ensureTenantOwner } from "../src/provisioning/index.js";
import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test, vi } from "vitest";
import { setupJournal } from "../src/provisioning/journal.js";
import { provisionOpenFGA } from "../src/provisioning/openfga.js";

test.skipIf(!process.env.HEIMDALL_TEST_SETUP_DSN || !process.env.HEIMDALL_PROBE_OPENFGA_KEY)(
  "OpenFGA recovery verifies explicit IDs and never repeats an ambiguous create",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_SETUP_DSN });
    const token = process.env.HEIMDALL_PROBE_OPENFGA_KEY!;
    const url = process.env.HEIMDALL_PROBE_OPENFGA_URL!;
    const signal = AbortSignal.timeout(25_000);
    const { config, manifest } = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/configuration-v1.json", import.meta.url),
        "utf8",
      ),
    );
    const scope = {
      productId: "ledger",
      environment: "fga-ts",
      stepCount: 4,
      fingerprint: "a".repeat(64),
    };
    const options = {
      scope,
      token,
      configJSON: JSON.stringify({
        ...config,
        environment: scope.environment,
        openfga: { ...config.openfga, url },
      }),
      manifestJSON: JSON.stringify(manifest),
    };
    const journal = setupJournal(pool, scope);
    const fetchReal = globalThis.fetch;
    let storeId = "";
    let modelId = "";
    let writes = 0;
    async function cleanup() {
      if (!storeId) return;
      const response = await fetchReal(url + "/stores/" + storeId, {
        method: "DELETE",
        redirect: "error",
        signal: AbortSignal.timeout(5000),
        headers: { authorization: "Bearer " + token },
      });
      if (!response.ok) throw new Error("disposable store cleanup failed");
    }
    try {
      for (const [ordinal, resource] of [
        [1, "verified-project"],
        [2, "verified-client"],
      ] as const) {
        const step = await journal.claim(ordinal, signal);
        await journal.confirm(ordinal, step.dispatchId, resource, signal);
      }
      await expect(
        provisionOpenFGA(pool, { ...options, recoverStoreId: "unknown" }, signal),
      ).rejects.toThrow("conflict");
      vi.stubGlobal("fetch", async (input: string, init: RequestInit) => {
        const response = await fetchReal(input, init);
        if (init.method !== "POST") return response;
        writes++;
        if (!response.ok) throw new Error("provider write failed");
        const body = await response.json();
        if (input.endsWith("/stores")) storeId = body.id;
        else modelId = body.authorization_model_id;
        throw new Error("discarded successful provider response");
      });
      await expect(provisionOpenFGA(pool, options, signal)).rejects.toThrow("outcome_unknown");
      expect(storeId).toBeTruthy();
      await expect(provisionOpenFGA(pool, options, signal)).rejects.toThrow("outcome_unknown");
      expect(writes).toBe(1);
      await expect(
        provisionOpenFGA(pool, { ...options, recoverStoreId: storeId }, signal),
      ).rejects.toThrow("outcome_unknown");
      expect(modelId).toBeTruthy();
      await expect(
        provisionOpenFGA(pool, { ...options, recoverModelId: storeId }, signal),
      ).rejects.toThrow("unavailable");
      const result = { url, storeId, modelId };
      expect(await provisionOpenFGA(pool, { ...options, recoverModelId: modelId }, signal)).toEqual(
        result,
      );
      expect(await provisionOpenFGA(pool, options, signal)).toEqual(result);
      expect(writes).toBe(2);
      vi.unstubAllGlobals();
      const runtimeConfig = JSON.stringify({ ...config, openfga: result });
      const owner = {
        operationId: "owner-1",
        tenantId: "acme",
        owner: { issuer: config.issuer, subject: "alice" },
      };
      const blocked = async () => {
        throw new Error("audit unavailable");
      };
      await expect(
        ensureTenantOwner(runtimeConfig, options.manifestJSON, token, owner, blocked, signal),
      ).rejects.toThrow("unavailable");
      let attempts = 0;
      const dispatch = async () => {
        attempts++;
      };
      vi.stubGlobal("fetch", async (input: string, init: RequestInit) => {
        const response = await fetchReal(input, init);
        if (!input.endsWith("/write")) return response;
        if (!response.ok) throw new Error("expected successful owner write");
        await response.body?.cancel();
        throw new Error("discarded owner write response");
      });
      await expect(
        ensureTenantOwner(runtimeConfig, options.manifestJSON, token, owner, dispatch, signal),
      ).rejects.toMatchObject({ code: "outcome_unknown", operationId: "owner-1" });
      vi.unstubAllGlobals();
      // The test interceptor has already received the provider response, so this write is quiescent.
      expect(
        await ensureTenantOwner(
          runtimeConfig,
          options.manifestJSON,
          token,
          owner,
          dispatch,
          signal,
        ),
      ).toMatchObject({ tenantId: "acme", modelId });
      await expect(
        ensureTenantOwner(
          runtimeConfig,
          options.manifestJSON,
          token,
          { ...owner, owner: { ...owner.owner, subject: "bob" } },
          dispatch,
          signal,
        ),
      ).rejects.toThrow("conflict");
      expect(attempts).toBe(1);
    } finally {
      vi.unstubAllGlobals();
      try {
        await cleanup();
      } finally {
        await pool.end();
      }
    }
  },
);
