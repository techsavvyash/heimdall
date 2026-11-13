import { encodeIdentity } from "./identity-encoding.js";
import { HeimdallError, checkCancellation } from "./errors.js";
import type { Intent } from "./operations.js";
import type { PinnedModel } from "./model-binding.js";

export async function planMembership(
  model: PinnedModel,
  ownerRole: string,
  intent: Intent,
  signal: AbortSignal,
) {
  const { kind, workspaceId, actor, target } = intent;
  if (!["add_member", "assign_roles", "remove_member"].includes(kind))
    throw new HeimdallError("invalid_argument");
  const verified = await model.verify(signal);
  const desired = new Set(intent.roles);
  for (const role of desired)
    if (role === ownerRole || !Object.hasOwn(verified.roles, role))
      throw new HeimdallError("invalid_argument");
  const user = encodeIdentity(target.issuer, target.subject);
  const admin = encodeIdentity(actor.issuer, actor.subject);
  const delegatedRoles = new Map<string, boolean>();
  async function canDelegate(role: string) {
    if (!delegatedRoles.has(role))
      delegatedRoles.set(role, await model.checkDelegation(admin, workspaceId, role, signal));
    return delegatedRoles.get(role)!;
  }
  let administrator = await model.checkRole(admin, workspaceId, ownerRole, signal);
  for (const role of Object.keys(verified.roles)) {
    if (administrator) break;
    administrator = await canDelegate(role);
  }
  if (!administrator) throw new HeimdallError("forbidden");
  const current = await model.directMembership(user, workspaceId, signal);
  if (current.has(verified.roles[ownerRole]!.assignment)) throw new HeimdallError("forbidden");
  const isMember = current.has("member");
  if (kind === "assign_roles" && !isMember) throw new HeimdallError("not_found");
  if (kind === "add_member" && isMember) throw new HeimdallError("conflict");
  const next = new Set<string>(kind === "remove_member" ? [] : ["member"]);
  for (const [name, relation] of Object.entries(verified.roles)) {
    const had = current.has(relation.assignment);
    const wants = desired.has(name);
    if (wants) next.add(relation.assignment);
    if (had !== wants || (kind === "add_member" && wants)) {
      if (!(await canDelegate(name))) throw new HeimdallError("forbidden");
    }
  }
  const writes = [...next].filter((relation) => !current.has(relation));
  const deletes = [...current].filter((relation) => !next.has(relation));
  return async (operationId: string, operation: AbortSignal) => {
    checkCancellation(operation);
    try {
      if (writes.length + deletes.length <= 100) {
        await model.writeDirectMembership(user, workspaceId, writes, deletes, operation);
      } else {
        // The managed gate stays closed for every batch of the same dispatch.
        for (let i = 0; i < deletes.length; i += 100)
          await model.writeDirectMembership(
            user,
            workspaceId,
            [],
            deletes.slice(i, i + 100),
            operation,
          );
        for (let i = 0; i < writes.length; i += 100)
          await model.writeDirectMembership(
            user,
            workspaceId,
            writes.slice(i, i + 100),
            [],
            operation,
          );
      }
      const observed = await model.directMembership(user, workspaceId, operation);
      if (observed.size !== next.size || [...next].some((relation) => !observed.has(relation)))
        throw new HeimdallError("unavailable");
    } catch {
      throw new HeimdallError("outcome_unknown", operationId);
    }
  };
}
