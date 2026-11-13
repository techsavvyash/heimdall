import type { Memberships } from "./managed-memberships.js";
import type { Principal } from "../runtime.js";
import { planDirectoryRevocation } from "../provisioning/directory-revocation.js";

export async function revokeDirectoryGrant(
  memberships: Memberships,
  actor: Principal,
  input: Parameters<typeof planDirectoryRevocation>[0] & {
    target: { issuer: string; subject: string };
    idempotencyKey: string;
  },
  signal: AbortSignal,
) {
  const plan = planDirectoryRevocation(input);
  const change = {
    target: input.target,
    idempotencyKey: input.idempotencyKey,
    expectedDirectRoles: input.currentRoles,
  };
  return plan.action === "remove_member"
    ? memberships.remove(actor, change, { signal })
    : memberships.assignRoles(actor, { ...change, roles: plan.roles }, { signal });
}
