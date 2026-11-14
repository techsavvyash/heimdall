import { Pool } from "pg";
import { expect, test, vi } from "vitest";
import { provisionApplication } from "../src/provisioning/application.js";
import { provisionProject } from "../src/provisioning/project.js";

test.skipIf(!process.env.HEIMDALL_TEST_SETUP_DSN || !process.env.HEIMDALL_PROBE_ZITADEL_PAT)(
  "ZITADEL project creation reconciles a lost response against the reserved ID",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_SETUP_DSN });
    const issuer = process.env.HEIMDALL_PROBE_ZITADEL_ISSUER!;
    const token = process.env.HEIMDALL_PROBE_ZITADEL_PAT!;
    const signal = AbortSignal.timeout(30_000);
    const fetchReal = globalThis.fetch;
    const call = async (path: string, body: object, method = "POST") => {
      const response = await fetchReal(issuer + path, {
        method,
        redirect: "error",
        signal,
        headers: {
          authorization: "Bearer " + token,
          "content-type": "application/json",
          "connect-protocol-version": "1",
        },
        ...(method === "POST" ? { body: JSON.stringify(body) } : {}),
      });
      expect(response.ok, "verification provider status").toBe(true);
      return response.json();
    };
    let organizationId: string | undefined;
    try {
      organizationId = (
        await call("/v2/organizations", { name: "heimdall-setup-" + crypto.randomUUID() })
      ).organizationId;
      expect(organizationId).toBeTruthy();
      let attempts = 0;
      let reservedId = "";
      vi.stubGlobal("fetch", async (input: string, init: RequestInit) => {
        const response = await fetchReal(input, init);
        if (!input.endsWith("/CreateProject")) return response;
        attempts++;
        expect(response.ok, "create with caller-selected project ID").toBe(true);
        reservedId = (await response.json()).projectId;
        expect(reservedId).toBe(JSON.parse(String(init.body)).projectId);
        throw new Error("discarded successful create response");
      });
      const options = {
        scope: {
          productId: "ledger",
          environment: "provider-ts",
          fingerprint: "b".repeat(64),
          stepCount: 3,
        },
        issuer,
        organizationId: organizationId!,
        name: "Ledger verification",
        token,
      };
      await expect(provisionProject(pool, options, signal)).rejects.toMatchObject({
        code: "outcome_unknown",
        operationId: expect.any(String),
      });
      expect(reservedId).toBeTruthy();
      expect(await provisionProject(pool, options, signal)).toBe(reservedId);
      expect(await provisionProject(pool, options, signal)).toBe(reservedId);
      let saved = "";
      let saves = 0;
      const appOptions = {
        ...options,
        ordinal: 2,
        projectId: reservedId,
        name: "web",
        redirectUris: ["https://ledger.example.invalid/callback"],
        async saveSecret(_applicationId: string, clientId: string, secret: string) {
          if (!secret) throw new Error("missing client secret");
          saved = clientId;
          saves++;
        },
        async hasSecret(_applicationId: string, clientId: string) {
          return saved === clientId;
        },
      };
      const app = await provisionApplication(pool, appOptions, signal);
      expect(app.clientId).toBe(saved);
      expect(await provisionApplication(pool, appOptions, signal)).toEqual(app);
      expect(saves).toBe(1);
      await expect(
        provisionApplication(pool, { ...appOptions, hasSecret: async () => false }, signal),
      ).rejects.toThrow("outcome_unknown");
      await expect(
        provisionApplication(
          pool,
          { ...appOptions, redirectUris: ["https://ledger.example.invalid/other"] },
          signal,
        ),
      ).rejects.toThrow("conflict");
      let lostApplicationCreates = 0;
      vi.stubGlobal("fetch", async (input: string, init: RequestInit) => {
        const response = await fetchReal(input, init);
        if (!input.endsWith("/CreateApplication")) return response;
        lostApplicationCreates++;
        if (!response.ok) throw new Error("expected application creation");
        await response.body?.cancel();
        throw new Error("discarded response containing the one-time secret");
      });
      const lost = { ...appOptions, ordinal: 3, name: "lost", hasSecret: async () => false };
      for (let retry = 0; retry < 2; retry++) {
        await expect(provisionApplication(pool, lost, signal)).rejects.toThrow("outcome_unknown");
      }
      expect(lostApplicationCreates).toBe(1);
      const absent = {
        ...options,
        scope: { ...options.scope, environment: "provider-unresolved" },
      };
      vi.stubGlobal("fetch", async (input: string, init: RequestInit) => {
        if (input.endsWith("/CreateProject")) {
          attempts++;
          throw new Error("connection lost before dispatch");
        }
        return fetchReal(input, init);
      });
      for (let attempt = 0; attempt < 2; attempt++) {
        await expect(provisionProject(pool, absent, signal)).rejects.toThrow("outcome_unknown");
      }
      expect(attempts).toBe(2);
      await expect(
        provisionProject(pool, { ...options, name: "Wrong name" }, signal),
      ).rejects.toThrow("conflict");
    } finally {
      vi.unstubAllGlobals();
      try {
        if (organizationId) await call("/v2/organizations/" + organizationId, {}, "DELETE");
      } finally {
        await pool.end();
      }
    }
  },
);
