import type { PostgresAudit } from "./audit.js";
import { checkCancellation, HeimdallError } from "../errors.js";
import { decodeRecoveryProposal, type RecoveryProposal } from "../recovery-proposal.js";

type RecoveryPort = Pick<
  PostgresAudit,
  "scanPendingDecisions" | "recoverRuntime" | "readResolution"
>;
// Trusted maintenance only. A proposal callback must authorize its initiator.
export async function recoverPendingDecisions(
  port: RecoveryPort,
  cutoff: number,
  limit: number,
  propose: (request: Record<string, unknown>) => RecoveryProposal,
  signal?: AbortSignal,
) {
  if (
    !Number.isSafeInteger(cutoff) ||
    cutoff < 0 ||
    !Number.isInteger(limit) ||
    limit < 1 ||
    limit > 100 ||
    typeof propose !== "function"
  )
    throw new HeimdallError("invalid_argument");
  const bounded = AbortSignal.any([AbortSignal.timeout(30_000), ...(signal ? [signal] : [])]);
  checkCancellation(bounded);
  // Restart at the head: terminal slots leave this durable queue automatically.
  const page = await port.scanPendingDecisions(cutoff, null, limit, bounded);
  const observed: {
    decisionId: string;
    state: string;
    terminalId: string;
    administrationId: string | null;
  }[] = [];
  for (const candidate of page.items) {
    checkCancellation(bounded);
    const original = JSON.parse(JSON.stringify(candidate.request)) as Record<string, unknown>;
    const proposal = decodeRecoveryProposal(
      original,
      JSON.stringify(propose(JSON.parse(JSON.stringify(original)))),
    );
    if (proposal.cutoff !== cutoff) throw new HeimdallError("invalid_argument");
    checkCancellation(bounded);
    let uncertain: HeimdallError | undefined;
    try {
      await port.recoverRuntime(proposal.terminal, proposal.administration, cutoff, bounded);
    } catch (error) {
      if (
        !(error instanceof HeimdallError) ||
        !["conflict", "outcome_unknown"].includes(error.code)
      )
        throw error;
      uncertain = error;
    }
    let resolution: Awaited<ReturnType<RecoveryPort["readResolution"]>>;
    try {
      resolution = await port.readResolution(candidate.decisionId, bounded);
    } catch (error) {
      // A failed observation cannot turn an uncertain commit into known failure.
      if (uncertain?.code === "outcome_unknown") throw uncertain;
      throw error;
    }
    if (!resolution || resolution.state === "pending")
      throw uncertain ?? new HeimdallError("audit_unavailable");
    observed.push({
      decisionId: candidate.decisionId,
      state: resolution.state,
      terminalId: resolution.terminal!.eventId as string,
      administrationId: (resolution.administration?.eventId as string | undefined) ?? null,
    });
  }
  return { observed, more: page.next !== null };
}
