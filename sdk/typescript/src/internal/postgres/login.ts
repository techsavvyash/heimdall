import { createHash, randomBytes } from "node:crypto";
import { checkCancellation, HeimdallError } from "../errors.js";
import { validClientId } from "../endpoints.js";
import { validateLoginClaim } from "../login-contract.js";
import type { LoginScope } from "../login-contract.js";
import type { PostgresRecords } from "./records.js";

type Target = Omit<LoginScope, "browserBindingDigest">;
type Payload = LoginScope & {
  schemaVersion: 1;
  createdAt: number;
  expiresAt: number;
  nonce: string;
  pkceVerifier: string;
} & ({ state: "pending" } | { state: "claimed"; claimedAt: number });

const lifetimeMs = 600_000;
const hash = (value: string) => createHash("sha256").update(value, "ascii");
const secret = () => randomBytes(32).toString("base64url");
const browserSecret = (value: string) =>
  typeof value === "string" &&
  value.length === 43 &&
  Buffer.from(value, "base64url").toString("base64url") === value;
const token = (value: unknown, max: number, min = 1, invalid = /[^A-Za-z0-9_-]/) =>
  typeof value === "string" && value.length >= min && value.length <= max && !invalid.test(value);
const bounded = (value: unknown, max: number) =>
  typeof value === "string" &&
  [...value].length > 0 &&
  [...value].length <= max &&
  !/[\uD800-\uDFFF]/u.test(value);
const instant = (value: unknown) =>
  typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
const shape = (
  value: unknown,
  fields: readonly string[],
  optional: readonly string[] = [],
): value is Record<string, unknown> =>
  value !== null &&
  typeof value === "object" &&
  !Array.isArray(value) &&
  fields.every((key) => Object.hasOwn(value, key)) &&
  Object.keys(value).every((key) => fields.includes(key) || optional.includes(key));

function validTarget(target: Target): boolean {
  const policy = target.tenantPolicy;
  return (
    token(target.productId, 64) &&
    token(target.environment, 64) &&
    bounded(target.issuer, 2048) &&
    validClientId(target.clientId) &&
    bounded(target.redirectUri, 2048) &&
    token(target.returnToKey, 64) &&
    (policy === null ||
      (shape(
        policy,
        ["tenantId", "organizationId", "policyVersion", "strictSSO"],
        ["providerId", "protocol"],
      ) &&
        token(policy.tenantId, 128) &&
        token(policy.organizationId, 128) &&
        token(policy.policyVersion, 128) &&
        typeof policy.strictSSO === "boolean" &&
        (Object.hasOwn(policy, "providerId") ||
        Object.hasOwn(policy, "protocol") ||
        policy.strictSSO
          ? bounded(policy.providerId, 128) &&
            (policy.protocol === "saml" || policy.protocol === "oidc")
          : true)))
  );
}

function decode(bytes: Uint8Array, expiresAt: number): Payload {
  let value: unknown;
  try {
    value = JSON.parse(new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes));
  } catch {
    throw new HeimdallError("unavailable");
  }
  if (
    !shape(
      value,
      [
        "schemaVersion",
        "productId",
        "environment",
        "issuer",
        "clientId",
        "redirectUri",
        "returnToKey",
        "browserBindingDigest",
        "tenantPolicy",
        "createdAt",
        "expiresAt",
        "nonce",
        "pkceVerifier",
        "state",
      ],
      ["claimedAt"],
    ) ||
    value.schemaVersion !== 1 ||
    !validTarget(value as unknown as Target) ||
    !instant(value.createdAt) ||
    !instant(value.expiresAt) ||
    value.expiresAt !== expiresAt ||
    !token(value.browserBindingDigest, 64, 64, /[^a-f0-9]/) ||
    !token(value.nonce, 128, 43) ||
    !token(value.pkceVerifier, 128, 43, /[^A-Za-z0-9._~-]/) ||
    !(
      (value.state === "pending" && !Object.hasOwn(value, "claimedAt")) ||
      (value.state === "claimed" && instant(value.claimedAt))
    )
  )
    throw new HeimdallError("unavailable");
  return value as unknown as Payload;
}

// Target comes from trusted routing and current tenant policy. The caller may only
// exchange a code after claim returns successfully.
export class BrowserLoginRecords {
  constructor(
    private readonly store: Pick<
      PostgresRecords,
      "create" | "get" | "compareAndSwap" | "matchesScope"
    >,
    private readonly now: () => number = Date.now,
  ) {}

  async start(target: Target, signal?: AbortSignal) {
    if (signal) checkCancellation(signal);
    const createdAt = this.now();
    const expiresAt = createdAt + lifetimeMs;
    if (
      !validTarget(target) ||
      !this.store.matchesScope(target.productId, target.environment) ||
      !instant(createdAt) ||
      !instant(expiresAt)
    )
      throw new HeimdallError("invalid_argument");
    const state = secret();
    const browserBinding = secret();
    const nonce = secret();
    const pkceVerifier = secret();
    const record: Payload = {
      schemaVersion: 1,
      productId: target.productId,
      environment: target.environment,
      issuer: target.issuer,
      clientId: target.clientId,
      redirectUri: target.redirectUri,
      returnToKey: target.returnToKey,
      tenantPolicy: target.tenantPolicy,
      browserBindingDigest: hash(browserBinding).digest("hex"),
      createdAt,
      expiresAt,
      nonce,
      pkceVerifier,
      state: "pending",
    };
    const created = await this.store.create(
      "login",
      state,
      Buffer.from(JSON.stringify(record)),
      expiresAt,
      signal,
    );
    if (!created) throw new HeimdallError("unauthenticated");
    if (signal) checkCancellation(signal);
    return {
      state,
      browserBinding,
      nonce,
      pkceChallenge: hash(pkceVerifier).digest("base64url"),
      expiresAt,
    };
  }

  async claim(target: Target, state: string, browserBinding: string, signal?: AbortSignal) {
    if (signal) checkCancellation(signal);
    if (!browserSecret(state) || !browserSecret(browserBinding))
      throw new HeimdallError("unauthenticated");
    if (!validTarget(target)) throw new HeimdallError("invalid_argument");
    if (!this.store.matchesScope(target.productId, target.environment))
      throw new HeimdallError("unauthenticated");
    const stored = await this.store.get("login", state, signal);
    if (!stored) throw new HeimdallError("unauthenticated");
    const record = decode(stored.value, stored.expiresAt);
    const now = this.now();
    validateLoginClaim(
      record,
      { ...target, browserBindingDigest: hash(browserBinding).digest("hex") },
      now,
    );
    const claimed: Payload = { ...record, state: "claimed", claimedAt: now };
    const updated = await this.store.compareAndSwap(
      "login",
      state,
      stored.revision,
      Buffer.from(JSON.stringify(claimed)),
      record.expiresAt,
      signal,
    );
    if (!updated) throw new HeimdallError("unauthenticated");
    if (signal) checkCancellation(signal);
    return { nonce: record.nonce, pkceVerifier: record.pkceVerifier };
  }
}
