import { readFileSync } from "node:fs";
import { Pool } from "pg";
import { PostgresAudit } from "../../src/internal/postgres/audit.js";
import { recoverPendingDecisions } from "../../src/internal/postgres/audit-maintenance.js";
import { buildRecoveryProposal } from "../../src/internal/recovery-proposal.js";
import { validateAuditShape } from "../../src/internal/audit-shape.js";
import { validateAuditEvent } from "../../src/internal/audit.js";
import assert from "node:assert/strict";
const mode = process.env.HEIMDALL_TEST_RECOVERY_RESTART_MODE;
const url = process.env.HEIMDALL_TEST_RECOVERY_RESTART_URL;
assert(url && ["seed", "crash", "resume"].includes(mode ?? ""));
const load = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const f = load("audit-capacity-writes");
const restart = load("recovery-restart");
const pool = new Pool({ connectionString: url, options: "-c role=public_capacity_runtime" });
const audit = new PostgresAudit(pool, restart.productId, restart.environment, ["siem"], (event) => {
  validateAuditShape(event);
  validateAuditEvent(event as Parameters<typeof validateAuditEvent>[0], "sdk", () => false);
});
try {
  if (mode === "seed") {
    for (const [i, decisionId] of restart.decisionIds.entries())
      await audit.appendRuntime({
        ...f.request,
        productId: restart.productId,
        environment: restart.environment,
        decisionId,
        eventId: restart.requestIds[i],
      });
  } else {
    let calls = 0;
    const propose = (request: Record<string, unknown>) => {
      calls++;
      if (mode === "crash" && calls === 2) process.kill(process.pid, "SIGKILL");
      return buildRecoveryProposal(request, {
        terminalId: `terminal-${mode}-${request.decisionId}`,
        requestId: `request-${mode}`,
        actor: { kind: "system", id: `${mode}-worker` },
        at: f.terminal.recordedAt,
        cutoff: f.cutoff,
        sdkVersion: "0.0.0",
      });
    };
    const result = await recoverPendingDecisions(audit, f.cutoff, 2, propose);
    assert.equal(mode, "resume", "crash worker must terminate before its second proposal");
    assert.equal(calls, 1);
    assert.equal(result.observed.length, 1);
    assert.equal(result.observed[0]?.decisionId, restart.decisionIds[1]);
    for (const [i, decisionId] of restart.decisionIds.entries()) {
      const record = await audit.readResolution(decisionId);
      const owner = i === 0 ? "crash" : "resume";
      assert.equal(record?.state, "recovered");
      assert.equal(record?.terminal?.eventId, `terminal-${owner}-${decisionId}`);
      assert.deepEqual(record?.administration?.actor, { kind: "system", id: `${owner}-worker` });
    }
    assert.deepEqual(await recoverPendingDecisions(audit, f.cutoff, 2, propose), {
      observed: [],
      more: false,
    });
    assert.equal(calls, 1);
  }
} finally {
  await pool.end();
}
