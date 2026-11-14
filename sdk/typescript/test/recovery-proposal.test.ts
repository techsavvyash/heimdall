import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import {
  buildRecoveryProposal,
  recoveryProposalDigest,
  decodeRecoveryProposal,
  validateRecoveryProposal,
  type RecoveryProposal,
} from "../src/internal/recovery-proposal.js";
const f = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/recovery-proposal.json", import.meta.url),
    "utf8",
  ),
);
test("recovery proposal preserves exact restart values and separates initiator", () => {
  const request = structuredClone(f.request),
    options = structuredClone(f.options);
  const proposal = buildRecoveryProposal(request, options);
  expect(proposal).toEqual(f.proposal);
  expect(recoveryProposalDigest(f.request, proposal)).toBe(f.digest);
  expect(decodeRecoveryProposal(f.request, JSON.stringify(proposal))).toEqual(f.proposal);
  validateRecoveryProposal(f.request, proposal);
  const other = buildRecoveryProposal(f.request, {
    ...f.options,
    actor: { kind: "system", id: "another-initiator" },
  });
  expect(recoveryProposalDigest(f.request, other)).not.toBe(f.digest);
  request.actor.subject = "changed";
  options.actor.id = "changed";
  expect(proposal).toEqual(f.proposal);
  expect(() => decodeRecoveryProposal(f.request, JSON.stringify(proposal) + " null")).toThrow(
    /invalid_argument|audit_unavailable/,
  );
  expect(() =>
    validateRecoveryProposal({ ...f.request, requestId: "different" }, proposal),
  ).toThrow(/invalid_argument|audit_unavailable/);
});
test.each(
  f.invalidOptions as { name: string; options: Parameters<typeof buildRecoveryProposal>[1] }[],
)("refuses recovery options $name", (c) => {
  expect(() => buildRecoveryProposal(f.request, c.options)).toThrow(
    /invalid_argument|audit_unavailable/,
  );
});
test.each(f.invalidProposals as { name: string; proposal: RecoveryProposal }[])(
  "refuses persisted proposal $name",
  (c) => {
    expect(() => decodeRecoveryProposal(f.request, JSON.stringify(c.proposal))).toThrow(
      /invalid_argument|audit_unavailable/,
    );
  },
);
