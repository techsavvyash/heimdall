import { createHash } from "node:crypto";
import { HeimdallError } from "./errors.js";
import { encodeIdentity } from "./identity-encoding.js";

type Identity = { issuer: string; subject: string };
export type Intent = {
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
const intentFields = [
  "schemaVersion",
  "productId",
  "environment",
  "kind",
  "actor",
  "target",
  "workspaceId",
  "name",
  "roles",
  "manifestVersion",
];
const identifier = (v: unknown, max: number, empty = false): v is string =>
  typeof v === "string" && (empty || v.length > 0) && v.length <= max && !/[^A-Za-z0-9_-]/.test(v);
function exactFields(value: unknown, keys: readonly string[]): value is Record<string, unknown> {
  return (
    !!value &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    keys.every((k) => Object.hasOwn(value, k)) &&
    Object.keys(value).every((k) => keys.includes(k))
  );
}
// Complete structural and semantic validation; this does not authorize the actor.
function validateIntent(value: unknown): asserts value is Intent {
  if (
    !exactFields(value, intentFields) ||
    value.schemaVersion !== 1 ||
    !identifier(value.productId, 64) ||
    !identifier(value.environment, 64) ||
    typeof value.kind !== "string" ||
    !["create_workspace", "add_member", "remove_member", "assign_roles", "transfer_owner"].includes(
      value.kind,
    ) ||
    !identifier(value.workspaceId, 128, true) ||
    typeof value.name !== "string" ||
    Array.from(value.name).length > 256 ||
    !Number.isInteger(value.manifestVersion) ||
    Number(value.manifestVersion) < 1 ||
    Number(value.manifestVersion) > 2147483647 ||
    !exactFields(value.actor, ["issuer", "subject"]) ||
    !exactFields(value.target, ["issuer", "subject"]) ||
    !Array.isArray(value.roles) ||
    value.roles.length > 128 ||
    new Set(value.roles).size !== value.roles.length ||
    value.roles.some(
      (r) => typeof r !== "string" || r.length > 64 || !/^[a-z][a-z0-9_]*(?![\s\S])/.test(r),
    )
  )
    throw new HeimdallError("invalid_argument");
  const i = value as Intent;
  const actor = encodeIdentity(i.actor.issuer, i.actor.subject);
  const target = encodeIdentity(i.target.issuer, i.target.subject);
  if (
    !i.name.isWellFormed() ||
    Array.from(i.name).some((c) => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127) ||
    Buffer.byteLength(i.name, "utf8") > 1024
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
}
// Reject Unicode repair, including escaped unpaired surrogates, before JSON decoding.
export function parseIntent(raw: string): Intent {
  if (typeof raw !== "string" || !raw.isWellFormed() || Buffer.byteLength(raw) > 1048576)
    throw new HeimdallError("invalid_argument");
  for (let i = 0; i < raw.length; i++) {
    if (raw[i] !== "\\") continue;
    if (raw[++i] !== "u") continue;
    const hex = raw.slice(i + 1, i + 5);
    if (!/^[a-fA-F0-9]{4}$/.test(hex)) throw new HeimdallError("invalid_argument");
    const n = parseInt(hex, 16);
    i += 4;
    if (n >= 0xdc00 && n <= 0xdfff) throw new HeimdallError("invalid_argument");
    if (n >= 0xd800 && n <= 0xdbff) {
      const low = raw.slice(i + 3, i + 7);
      if (
        raw.slice(i + 1, i + 3) !== "\\u" ||
        !/^[a-fA-F0-9]{4}$/.test(low) ||
        parseInt(low, 16) < 0xdc00 ||
        parseInt(low, 16) > 0xdfff
      )
        throw new HeimdallError("invalid_argument");
      i += 6;
    }
  }
  try {
    const value: unknown = JSON.parse(raw);
    validateIntent(value);
    return value;
  } catch {
    throw new HeimdallError("invalid_argument");
  }
}
// Scope keys exclude the allocated create-workspace ID. Validation is never optional.
export function intentDigests(
  i: Intent,
  idempotencyKey: string,
): { fingerprint: string; keyDigest: string } {
  validateIntent(i);
  if (
    typeof idempotencyKey !== "string" ||
    !idempotencyKey.isWellFormed() ||
    !idempotencyKey ||
    Buffer.byteLength(idempotencyKey) > 256
  )
    throw new HeimdallError("invalid_argument");
  const actor = encodeIdentity(i.actor.issuer, i.actor.subject);
  const target = encodeIdentity(i.target.issuer, i.target.subject);
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

// Stored payload checksums bind to indexed journal columns; they do not authorize.
export function parseBoundIntent(
  raw: string,
  binding: {
    productId: string;
    environment: string;
    workspaceId: string;
    kind: string;
    actorHash: string;
    targetHash: string;
    fingerprint: string;
    manifestVersion: number;
  },
): Intent {
  try {
    const intent = parseIntent(raw);
    const hash = (identity: Identity) =>
      createHash("sha256")
        .update(encodeIdentity(identity.issuer, identity.subject), "ascii")
        .digest("hex");
    if (
      !identifier(binding.workspaceId, 128) ||
      intent.productId !== binding.productId ||
      intent.environment !== binding.environment ||
      intent.kind !== binding.kind ||
      intent.manifestVersion !== binding.manifestVersion ||
      (intent.kind !== "create_workspace" && intent.workspaceId !== binding.workspaceId) ||
      hash(intent.actor) !== binding.actorHash ||
      hash(intent.target) !== binding.targetHash ||
      intentDigests(intent, "binding-check").fingerprint !== binding.fingerprint
    )
      throw new Error();
    return intent;
  } catch {
    throw new HeimdallError("unavailable");
  }
}
