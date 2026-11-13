import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { validateAuditEvent } from "../src/internal/audit.js";
import { PostgresAudit } from "../src/internal/postgres/audit.js";
import { OperationRecords } from "../src/internal/postgres/operations.js";
import { WorkspaceRecords } from "../src/internal/postgres/workspaces.js";

const url = process.env.HEIMDALL_TEST_RESTORED_STORAGE_URL;
test.skipIf(!url)(
  "reads retained decisions and blocked operations after PostgreSQL restore",
  async () => {
    const pool = new Pool({ connectionString: url });
    try {
      const audit = (productId: string, environment: string) =>
        PostgresAudit.forRetainedRuntime(pool, productId, environment, ["siem"], (event) => {
          validateAuditShape(event);
          validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
        });
      expect(await audit("ledger", "production").readResolution("decision-1")).toMatchObject({
        state: "pending",
        terminal: null,
      });
      const retired = audit("ledger", "staging");
      expect(await retired.inspect("tenant-1", "z-request")).toMatchObject({
        state: "retired",
        event: null,
      });
      const original = JSON.parse(
        readFileSync(
          new URL("../../../contracts/fixtures/audit-capacity-writes.json", import.meta.url),
          "utf8",
        ),
      );
      expect(
        await retired.resolveReceipt({ ...original.request, environment: "staging" }),
      ).toMatchObject({
        eventId: "z-request",
        environment: "staging",
        recordedAt: original.request.recordedAt,
      });
      await expect(retired.readResolution("decision-1")).rejects.toMatchObject({
        code: "not_found",
      });
      expect(
        await audit("holds-go", "production").eligibility("tenant-1", "ordinary", 100000, 1000),
      ).toBe("retain_retry");
      const fixture = JSON.parse(
        readFileSync(
          new URL("../../../contracts/fixtures/operation-journal.json", import.meta.url),
          "utf8",
        ),
      );
      const binding = { ...fixture.binding, productId: "transition-go" };
      const operations = OperationRecords.forRetainedRuntime(pool, binding, ["primary"]);
      expect(await operations.get("safe-retry", fixture.createIntent.actor)).toMatchObject({
        state: "uncertain",
        workspaceId: "safe-retry",
      });
      const workspaces = WorkspaceRecords.forRetainedRuntime(
        pool,
        binding.productId,
        binding.environment,
      );
      expect(await workspaces.get("safe-retry")).toMatchObject({ mutationBlocked: true });
    } finally {
      await pool.end();
    }
  },
);
