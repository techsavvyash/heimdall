import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { recoverPendingDecisions } from "../src/internal/postgres/audit-maintenance.js";
import { buildRecoveryProposal } from "../src/internal/recovery-proposal.js";
import { HeimdallError } from "../src/internal/errors.js";
const load = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const f = load("audit-capacity-writes");
const propose = (request: Record<string, unknown>) =>
  buildRecoveryProposal(request, {
    terminalId: f.terminal.eventId,
    requestId: f.administration.requestId,
    actor: f.administration.actor,
    at: f.terminal.recordedAt,
    cutoff: f.cutoff,
    sdkVersion: "0.0.0",
  });
for (const c of load("recovery-batch").cases) {
  test(`recovery batch: ${c.name}`, async () => {
    const calls: string[] = [];
    const port = {
      async scanPendingDecisions() {
        calls.push("scan");
        return {
          items: [
            {
              decisionId: f.request.decisionId,
              requestedAt: f.request.recordedAt,
              request: f.request,
            },
          ],
          next: null,
        };
      },
      async recoverRuntime() {
        calls.push("write");
        if (c.writeError) throw new HeimdallError(c.writeError);
        return {
          terminal: {
            eventId: "terminal",
            recordedAt: 1,
            productId: "ledger",
            environment: "production",
            payloadDigest: "unused",
          },
          administration: {
            eventId: "admin",
            recordedAt: 1,
            productId: "ledger",
            environment: "production",
            payloadDigest: "unused",
          },
        };
      },
      async readResolution() {
        calls.push("read");
        if (c.readError) throw new HeimdallError(c.readError);
        return c.readState === "absent"
          ? null
          : {
              decisionId: f.request.decisionId,
              state: c.readState,
              request: f.request,
              terminal: c.readState === "pending" ? null : { ...f.terminal, eventId: "winner" },
              administration:
                c.readState === "recovered"
                  ? { ...f.administration, eventId: "winner-admin" }
                  : null,
            };
      },
    };
    const result = await recoverPendingDecisions(port, f.cutoff, 1, propose).then(
      (value) => ({ value, error: "" }),
      (error) => ({ value: null, error: error.code }),
    );
    expect(result.error).toBe(c.expectedError);
    expect(result.value?.observed ?? []).toEqual(
      c.expectedError
        ? []
        : [
            {
              decisionId: f.request.decisionId,
              state: c.readState,
              terminalId: "winner",
              administrationId: c.readState === "recovered" ? "winner-admin" : null,
            },
          ],
    );
    expect(calls).toEqual(
      c.writeError === "unavailable" ? ["scan", "write"] : ["scan", "write", "read"],
    );
  });
}

test("invalid limits and cancellation stop before database access", async () => {
  const fail = async (): Promise<never> => {
    throw new Error("unexpected database access");
  };
  const port = { scanPendingDecisions: fail, recoverRuntime: fail, readResolution: fail };
  for (const limit of [0, 101, -1])
    await expect(recoverPendingDecisions(port, f.cutoff, limit, propose)).rejects.toThrow(
      "invalid_argument",
    );
  await expect(
    recoverPendingDecisions(port, f.cutoff, 1, propose, AbortSignal.abort()),
  ).rejects.toThrow("cancelled");
});
