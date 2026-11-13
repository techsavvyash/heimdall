import { Pool } from "pg";
import { expect, test } from "vitest";
import { setupJournal } from "../src/provisioning/journal.js";

test.skipIf(!process.env.HEIMDALL_TEST_SETUP_DSN)(
  "setup journal returns only committed claims",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_SETUP_DSN });
    const signal = AbortSignal.timeout(10_000);
    try {
      const scope = {
        productId: "ledger",
        environment: "setup-ts",
        fingerprint: "a".repeat(64),
        stepCount: 2,
      };
      const journal = setupJournal(pool, scope);
      await journal.ready(signal);
      const claims = await Promise.all([journal.claim(1, signal), journal.claim(1, signal)]);
      const winner = claims.find((step) => step.state === "claimed")!;
      expect(claims.map((step) => step.state).sort()).toEqual(["claimed", "unresolved"]);
      expect(claims.every((step) => step.dispatchId === winner.dispatchId)).toBe(true);
      await expect(journal.claim(2, signal)).rejects.toThrow("conflict");
      await journal.confirm(1, winner.dispatchId, "project_1", signal);
      await journal.confirm(1, winner.dispatchId, "project_1", signal);
      await expect(journal.confirm(1, winner.dispatchId, "project_2", signal)).rejects.toThrow(
        "conflict",
      );
      expect(await journal.claim(1, signal)).toEqual({
        state: "confirmed",
        dispatchId: winner.dispatchId,
        resourceId: "project_1",
      });
      expect((await journal.claim(2, signal)).state).toBe("claimed");
    } finally {
      await pool.end();
    }
  },
);
