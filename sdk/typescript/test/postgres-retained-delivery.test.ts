import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { expect, test } from "vitest";
import { validateAuditShape } from "../src/internal/audit-shape.js";
import { PostgresAudit } from "../src/internal/postgres/audit.js";

const url = process.env.HEIMDALL_TEST_RETAINED_DELIVERY_URL;
const rejected = process.env.HEIMDALL_TEST_RETAINED_DELIVERY_REJECT === "1";
function setup() {
  const pool = new Pool({ connectionString: url });
  const port = PostgresAudit.forRetainedRuntime(
    pool,
    "ledger",
    "delivery-ts",
    ["siem"],
    validateAuditShape,
  );
  const batch = {
    productId: "ledger",
    environment: "delivery-ts",
    sinkId: "siem",
    eventIds: ["ordinary"],
  };
  return { pool, port, batch };
}
test.skipIf(!url || rejected)("gates retained delivery reads and writes", async () => {
  const { pool, port, batch } = setup();
  try {
    const fixture = JSON.parse(
      readFileSync(
        new URL("../../../contracts/fixtures/audit-capacity-writes.json", import.meta.url),
        "utf8",
      ),
    );
    await port.append({ ...fixture.ordinary, environment: "delivery-ts" });
    expect(await port.pending("siem", 2000, 8)).toMatchObject([
      { eventId: "ordinary", event: { eventId: "ordinary" } },
    ]);
    expect(await port.beginAttempt("siem", "ordinary", 0, 2000, 3000)).toBe(1);
    expect(await port.deferAttempt("siem", "ordinary", 1, 4000)).toBe(true);
    await port.acknowledge(batch, batch, 5000);
    expect(await port.pending("siem", 6000, 8)).toEqual([]);
  } finally {
    await pool.end();
  }
});

test.skipIf(!url || !rejected)("refuses delivery during incompatible storage", async () => {
  const { pool, port, batch } = setup();
  try {
    await expect(port.pending("siem", 2000, 8)).rejects.toThrow("unavailable");
    await expect(port.beginAttempt("siem", "ordinary", 0, 2000, 3000)).rejects.toThrow(
      "unavailable",
    );
    await expect(port.deferAttempt("siem", "ordinary", 1, 4000)).rejects.toThrow("unavailable");
    await expect(port.quarantine("siem", "ordinary", 0)).rejects.toThrow("unavailable");
    await expect(port.acknowledge(batch, batch, 5000)).rejects.toThrow("unavailable");
  } finally {
    await pool.end();
  }
});
