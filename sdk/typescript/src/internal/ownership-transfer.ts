import type { PinnedModel } from "./model-binding.js";
import type { Intent } from "./operations.js";
import { encodeIdentity } from "./identity-encoding.js";
import { HeimdallError } from "./errors.js";

export async function planOwnershipTransfer(
  model: PinnedModel,
  ownerRole: string,
  intent: Intent,
  signal: AbortSignal,
  recovering = false,
) {
  const { actor, target, workspaceId } = intent;
  const source = encodeIdentity(actor.issuer, actor.subject);
  const recipient = encodeIdentity(target.issuer, target.subject);
  if (source === recipient) throw new HeimdallError("invalid_argument");
  const verified = await model.verify(signal);
  const owner = verified.roles[ownerRole]!.assignment;
  if (!recovering && !(await model.checkRole(source, workspaceId, ownerRole, signal)))
    throw new HeimdallError("forbidden");
  const current = await model.directMembership(recipient, workspaceId, signal);
  if (!current.has("member")) throw new HeimdallError("conflict");
  const previous = await model.directMembership(source, workspaceId, signal);
  if (!current.has(owner) && (!previous.has(owner) || !previous.has("member")))
    throw new HeimdallError("conflict");
  return async (id: string, operation: AbortSignal) => {
    try {
      if (!current.has(owner))
        await model.writeDirectMembership(recipient, workspaceId, [owner], [], operation);
      const confirmed = await model.directMembership(recipient, workspaceId, operation);
      if (!confirmed.has("member") || !confirmed.has(owner)) throw new Error();
      if (previous.has(owner))
        await model.writeDirectMembership(source, workspaceId, [], [owner], operation);
      const former = await model.directMembership(source, workspaceId, operation);
      if (former.has(owner)) throw new Error();
    } catch {
      throw new HeimdallError("outcome_unknown", id);
    }
  };
}
