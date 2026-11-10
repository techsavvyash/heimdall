import { createHash } from "node:crypto";
import { HeimdallError } from "./errors.js";
import { encodeIdentity } from "./identity-encoding.js";

type Identity = { issuer: string; subject: string };
type Intent = {
  schemaVersion: 1;
  productId: string;
  environment: string;
  kind: string;
  actor: Identity;
  target: Identity;
  workspaceId: string;
  name: string;
  roles: string[];
  manifestVersion: number;
};
function digest(value: unknown): string {
  const canonical = JSON.stringify(value).replace(
    /[\u007f-\uffff]/g,
    (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"),
  );
  return createHash("sha256").update(canonical, "ascii").digest("hex");
}
// Structural validation first; scope keys exclude the allocated create-workspace ID.
export function intentDigests(
  i: Intent,
  idempotencyKey: string,
): { fingerprint: string; keyDigest: string } {
  const actor = encodeIdentity(i.actor.issuer, i.actor.subject);
  const target = encodeIdentity(i.target.issuer, i.target.subject);
  if (
    !i.name.isWellFormed() ||
    Array.from(i.name).some((c) => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127) ||
    Buffer.byteLength(i.name, "utf8") > 1024 ||
    !idempotencyKey.isWellFormed() ||
    !idempotencyKey ||
    Buffer.byteLength(idempotencyKey, "utf8") > 256
  ) {
    throw new HeimdallError("invalid_argument");
  }
  if (i.kind === "create_workspace") {
    if (
      i.workspaceId !== "" ||
      /^[\s\u0085]*$/u.test(i.name) ||
      i.roles.length !== 0 ||
      actor !== target
    )
      throw new HeimdallError("invalid_argument");
  } else if (
    !i.workspaceId ||
    i.name !== "" ||
    (["remove_member", "transfer_owner"].includes(i.kind) && i.roles.length !== 0)
  ) {
    throw new HeimdallError("invalid_argument");
  }
  return {
    fingerprint: digest([
      "heimdall.intent.v1",
      i.productId,
      i.environment,
      i.kind,
      actor,
      target,
      i.workspaceId,
      i.name,
      [...i.roles].sort(),
      i.manifestVersion,
    ]),
    keyDigest: digest([
      "heimdall.idempotency.v1",
      i.productId,
      i.environment,
      i.kind,
      actor,
      idempotencyKey,
    ]),
  };
}
const transitions: Readonly<Record<string, Readonly<Record<string, string>>>> = {
  pending: { dispatch: "dispatching", pre_dispatch_failure: "retryable", cancel: "cancelled" },
  retryable: { retry: "pending", cancel: "cancelled" },
  dispatching: { confirm: "confirmed", uncertain: "uncertain", rejected: "retryable" },
  confirmed: { complete: "completed" },
};
// This computes a proposed transition. The database must enforce generation CAS.
export function nextOperationState(
  state: string,
  event: string,
  dispatchCount: number,
  generation: number,
  expectedGeneration: number,
): string {
  if (
    ![dispatchCount, generation, expectedGeneration].every(Number.isSafeInteger) ||
    dispatchCount < 0 ||
    generation < 1 ||
    expectedGeneration < 1
  )
    throw new HeimdallError("invalid_argument");
  if (generation !== expectedGeneration || (event === "cancel" && dispatchCount !== 0))
    throw new HeimdallError("conflict");
  if (!Object.hasOwn(transitions, state)) throw new HeimdallError("conflict");
  const events = transitions[state]!;
  if (!Object.hasOwn(events, event)) throw new HeimdallError("conflict");
  return events[event]!;
}
