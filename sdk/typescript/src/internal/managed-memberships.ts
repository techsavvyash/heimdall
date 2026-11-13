import { encodeIdentity } from "./identity-encoding.js";
import { randomUUID } from "node:crypto";
import type { IdentityGate, Principal } from "../runtime.js";
import type { AuditedAuthentication } from "./audited-authentication.js";
import type { PinnedModel } from "./model-binding.js";
import { HeimdallError, checkCancellation } from "./errors.js";
import { planOwnershipTransfer } from "./ownership-transfer.js";
import { planMembership } from "./membership-mutation.js";
import { intentDigests, type Intent } from "./operations.js";
import type { ManagedOperation, OperationRecords } from "./postgres/operations.js";
import type { WorkspaceRecords } from "./postgres/workspaces.js";

export type MembershipInput = Readonly<{
  target: Readonly<Intent["target"]>;
  idempotencyKey: string;
}>;
type RolesInput = MembershipInput & { readonly roles: readonly string[] };
type Call = { signal: AbortSignal };
export type MembershipStatus = Readonly<{
  operationId: string;
  workspaceId: string;
  status: "confirmed" | "pending";
  reason?: "recovery_required";
}>;
export type Memberships = Readonly<{
  authorizeInvitation(actor: Principal, input: RolesInput, call: Call): Promise<void>;
  /** The product must first claim a durable, recipient-bound invitation. */
  acceptInvitation(
    recipient: Principal,
    inviter: Intent["actor"],
    input: RolesInput,
    call: Call,
  ): Promise<MembershipStatus>;
  add(actor: Principal, input: RolesInput, call: Call): Promise<MembershipStatus>;
  assignRoles(actor: Principal, input: RolesInput, call: Call): Promise<MembershipStatus>;
  transferOwnership(
    actor: Principal,
    input: MembershipInput,
    call: Call,
  ): Promise<MembershipStatus>;
  remove(actor: Principal, input: MembershipInput, call: Call): Promise<MembershipStatus>;
}>;

function status(op: ManagedOperation): MembershipStatus {
  return {
    operationId: op.operationId,
    workspaceId: op.workspaceId,
    status: op.state === "completed" ? "confirmed" : "pending",
    ...(op.state === "uncertain" || op.state === "dispatching"
      ? { reason: "recovery_required" }
      : {}),
  };
}

export function managedMemberships(
  workspaceId: string,
  config: {
    productId: string;
    environment: string;
    issuer: string;
    manifestVersion: number;
    openfga: { modelId: string };
  },
  ownerRole: string,
  policyVersion: string,
  records: OperationRecords,
  workspaces: Pick<WorkspaceRecords, "get">,
  auth: AuditedAuthentication,
  model: PinnedModel,
  gate: IdentityGate,
): Memberships {
  async function mutate(
    kind: "add_member" | "assign_roles" | "remove_member" | "transfer_owner",
    principal: Principal,
    input: MembershipInput | RolesInput,
    call: Call,
    inviter?: Intent["actor"],
    authorizeOnly = false,
  ): Promise<MembershipStatus | undefined> {
    if (!(call?.signal instanceof AbortSignal) || !input?.target)
      throw new HeimdallError("invalid_argument");
    const signal = AbortSignal.any([call.signal, AbortSignal.timeout(30000)]);
    checkCancellation(signal);
    if (!auth.owns(principal)) throw new HeimdallError("unauthenticated");
    const roles = "roles" in input && Array.isArray(input.roles) ? [...input.roles] : [];
    if (
      ((kind === "add_member" || kind === "assign_roles") &&
        !Array.isArray((input as RolesInput).roles)) ||
      input.target.issuer !== config.issuer
    )
      throw new HeimdallError("invalid_argument");
    const actor = { issuer: principal.issuer, subject: principal.subject };
    const intent: Intent = {
      schemaVersion: 1,
      productId: config.productId,
      environment: config.environment,
      kind,
      workspaceId,
      actor,
      target: { issuer: input.target.issuer, subject: input.target.subject },
      name: "",
      roles,
      manifestVersion: config.manifestVersion,
    };
    if (
      inviter &&
      (inviter.issuer !== config.issuer ||
        actor.issuer !== intent.target.issuer ||
        actor.subject !== intent.target.subject)
    )
      throw new HeimdallError("forbidden");
    const sponsor = inviter && { ...inviter };
    if (sponsor) encodeIdentity(sponsor.issuer, sponsor.subject);
    const key = input.idempotencyKey;
    intentDigests(intent, key);
    let existing: ManagedOperation | null = null;
    let revision = "";
    let apply: Awaited<ReturnType<typeof planMembership>> | undefined;
    const allowed = await auth.checkPermission(
      principal,
      workspaceId,
      authorizeOnly
        ? "invitation.issue"
        : sponsor
          ? "invitation.accept"
          : kind === "transfer_owner"
            ? "ownership.transfer"
            : "membership." + kind,
      config.openfga.modelId,
      config.manifestVersion,
      async (operation) => {
        if (!authorizeOnly) existing = await records.findIntent(intent, key, operation);
        if (existing && existing.state !== "pending") return true;
        const workspace = await workspaces.get(workspaceId, operation);
        if (
          !workspace ||
          workspace.lifecycle !== "active" ||
          workspace.mutationBlocked !== !!existing
        )
          return false;
        if (sponsor && (await gate.check(sponsor, operation)) !== false) return false;
        revision = workspace.revision;
        apply =
          kind === "transfer_owner"
            ? await planOwnershipTransfer(model, ownerRole, intent, operation)
            : await planMembership(
                model,
                ownerRole,
                sponsor ? { ...intent, actor: sponsor } : intent,
                operation,
              );
        return true;
      },
      signal,
    );
    if (!allowed) throw new HeimdallError("forbidden");
    if (authorizeOnly) return;
    const requestId = randomUUID();
    function metadata(floor = 0) {
      const now = Math.max(Date.now(), floor);
      return {
        requestId,
        policyVersion,
        eventId: randomUUID(),
        occurredAt: now,
        recordedAt: now,
        nextAttemptAt: now,
      };
    }
    let op: ManagedOperation | null = existing;
    const operationId = randomUUID();
    if (!op) {
      op = await records.beginMembership(
        JSON.stringify(intent),
        key,
        {
          workspaceId,
          operationId,
          ...metadata(),
        },
        revision,
        signal,
      );
      if (op.operationId !== operationId) return status(op);
    }
    async function advance(before: ManagedOperation, transition: string) {
      return records.advance(
        before.operationId,
        actor,
        before.generation,
        transition,
        metadata(before.updatedAt),
        signal,
      );
    }
    try {
      if (op.state === "pending" && apply) {
        op = await advance(op, "dispatch");
        try {
          await apply(op.operationId, signal);
        } catch {
          if (!signal.aborted) op = await advance(op, "uncertain");
          return status(op);
        }
        op = await advance(op, "confirm");
      }
      if (op.state === "confirmed") op = await advance(op, "complete");
      return status(op);
    } catch (error) {
      if (error instanceof HeimdallError && error.code === "conflict" && !signal.aborted) {
        const current = await records.get(op.operationId, actor, signal);
        if (current && current.generation !== op.generation) return status(current);
      }
      throw error;
    }
  }
  const execute = async (...args: Parameters<typeof mutate>) => (await mutate(...args))!;
  return Object.freeze({
    async authorizeInvitation(actor, input, call) {
      await mutate("add_member", actor, input, call, undefined, true);
    },
    async acceptInvitation(recipient, inviter, input, call) {
      if (!inviter) throw new HeimdallError("invalid_argument");
      return execute("add_member", recipient, input, call, inviter);
    },
    add: (actor, input, call) => execute("add_member", actor, input, call),
    assignRoles: (actor, input, call) => execute("assign_roles", actor, input, call),
    transferOwnership: (actor, input, call) => execute("transfer_owner", actor, input, call),
    remove: (actor, input, call) => execute("remove_member", actor, input, call),
  });
}
