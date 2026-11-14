import { validClientId } from "./endpoints.js";
import { createHash } from "node:crypto";
import { TokenVerifier } from "./token-verifier.js";
import { HeimdallError, checkCancellation } from "./errors.js";
import { encodeIdentity } from "./identity-encoding.js";

type Config = ConstructorParameters<typeof TokenVerifier>[0] & {
  productId: string;
  environment: string;
  policyVersion: string;
  clientId: string;
  freshnessMs?: number;
};
type Identity = Readonly<{ issuer: string; subject: string }>;
type Negative = Identity & {
  context: string;
  tokenDigest: string;
  reason: "token_inactive" | "identity_inactive" | "identity_deleted";
};
type Gate = {
  check: (identity: Identity, signal: AbortSignal) => Promise<boolean>;
  invalidate: (negative: Negative, signal: AbortSignal) => Promise<void>;
};
type Principal = Readonly<
  Identity & {
    kind: "human";
    productId: string;
    environment: string;
    policyVersion: string;
    identityValidatedAt: number;
    expiresAt: number;
  }
>;
type Entry = { principal: Principal; identity: string; digest: string; until: number };
// Internal composition: callers must supply a durable local deny/session gate.
export class IdentityVerifier {
  private readonly config: Config;
  private readonly tokens: TokenVerifier;
  private readonly freshness: number;
  private readonly cache = new Map<string, Entry>();
  private readonly inflight = new Set<{
    identity: string;
    digest: string;
    reason: Negative["reason"] | undefined;
  }>();
  private readonly principals = new WeakSet<object>();
  constructor(
    config: Config,
    private readonly secret: (signal: AbortSignal) => Promise<string>,
    private readonly gate: Gate,
    private readonly now: () => number = Date.now,
  ) {
    if (
      ![config.productId, config.environment, config.policyVersion].every(
        (v) => typeof v === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(v),
      ) ||
      !validClientId(config.clientId) ||
      typeof secret !== "function" ||
      typeof gate?.check !== "function" ||
      typeof gate?.invalidate !== "function"
    )
      throw new HeimdallError("invalid_argument");
    this.freshness = config.freshnessMs ?? 60000;
    if (!Number.isSafeInteger(this.freshness) || this.freshness < 0 || this.freshness > 60000)
      throw new HeimdallError("invalid_argument");
    this.gate = { check: gate.check.bind(gate), invalidate: gate.invalidate.bind(gate) };
    this.config = { ...config };
    this.tokens = new TokenVerifier(config, now);
  }
  owns(value: unknown) {
    return typeof value === "object" && value !== null && this.principals.has(value);
  }
  sessionPrincipal(principal: Principal): Principal {
    if (!this.owns(principal)) throw new HeimdallError("unauthenticated");
    const copy = Object.freeze({ ...principal });
    this.principals.add(copy);
    return copy;
  }
  private async bounded<T>(
    action: () => Promise<T>,
    signal: AbortSignal,
    mutation = false,
  ): Promise<T> {
    checkCancellation(signal);
    let onAbort: (() => void) | undefined;
    try {
      return await Promise.race([
        Promise.resolve().then(action),
        new Promise<never>((_resolve, reject) => {
          onAbort = () => {
            try {
              checkCancellation(signal);
            } catch (e) {
              reject(mutation ? new HeimdallError("outcome_unknown") : e);
            }
          };
          signal.addEventListener("abort", onAbort, { once: true });
          if (signal.aborted) onAbort();
        }),
      ]);
    } catch (e) {
      if (e instanceof HeimdallError) throw e;
      checkCancellation(signal);
      throw new HeimdallError("unavailable");
    } finally {
      if (onAbort) signal.removeEventListener("abort", onAbort);
    }
  }
  private async json(path: string, options: RequestInit, signal: AbortSignal) {
    try {
      const response = await fetch(this.config.issuer.replace(/\/$/, "") + path, {
        ...options,
        redirect: "error",
        signal,
      });
      if (response.status !== 200) {
        await response.body?.cancel();
        return { status: response.status, body: null };
      }
      if (!response.body) throw new Error();
      const reader = response.body.getReader();
      let size = 0;
      const parts: Uint8Array[] = [];
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          size += value.length;
          if (size > 65536) throw new Error();
          parts.push(value);
        }
      } finally {
        await reader.cancel();
      }
      const body: unknown = JSON.parse(
        new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(Buffer.concat(parts)),
        (key, value: unknown) => {
          if (!key.isWellFormed() || (typeof value === "string" && !value.isWellFormed()))
            throw new Error();
          return value;
        },
      );
      if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error();
      return { status: response.status, body: body as Record<string, unknown> };
    } catch {
      checkCancellation(signal);
      throw new HeimdallError("unavailable");
    }
  }
  private async localDenied(identity: Identity, signal: AbortSignal) {
    const denied = await this.bounded(() => this.gate.check(identity, signal), signal);
    if (typeof denied !== "boolean") throw new HeimdallError("unavailable");
    return denied;
  }
  async checkCurrent(principal: Principal, signal?: AbortSignal): Promise<void> {
    if (!this.owns(principal)) throw new HeimdallError("invalid_argument");
    const bounded = AbortSignal.any([AbortSignal.timeout(5000), ...(signal ? [signal] : [])]);
    checkCancellation(bounded);
    const denied = await this.localDenied(
      { issuer: principal.issuer, subject: principal.subject },
      bounded,
    );
    checkCancellation(bounded);
    const now = this.now();
    if (denied || now >= principal.expiresAt) throw new HeimdallError("unauthenticated");
    if (
      now < principal.identityValidatedAt ||
      now - principal.identityValidatedAt >= (this.freshness || 5000)
    )
      throw new HeimdallError("unavailable");
  }
  async verify(raw: string, context = "bearer", signal?: AbortSignal): Promise<Principal> {
    const bounded = AbortSignal.any([AbortSignal.timeout(5000), ...(signal ? [signal] : [])]);
    checkCancellation(bounded);
    if (!/^[A-Za-z0-9:_-]{1,256}$/.test(context)) throw new HeimdallError("invalid_argument");
    const token = await this.tokens.verify(raw, bounded);
    const identity = encodeIdentity(token.issuer, token.subject);
    const digest = createHash("sha256").update(raw, "ascii").digest("hex");
    const key = createHash("sha256")
      .update(
        JSON.stringify([
          this.config.productId,
          this.config.environment,
          this.config.policyVersion,
          identity,
          digest,
          context,
        ]),
      )
      .digest("hex");
    if (await this.localDenied(token, bounded)) throw new HeimdallError("unauthenticated");
    const cached = this.cache.get(key);
    if (cached && this.now() >= cached.principal.identityValidatedAt && this.now() < cached.until) {
      checkCancellation(bounded);
      return cached.principal;
    }
    this.cache.delete(key);
    const started = this.now();
    const observation = { identity, digest, reason: undefined as Negative["reason"] | undefined };
    this.inflight.add(observation);
    try {
      const deny = async (reason: Negative["reason"]): Promise<never> => {
        for (const active of this.inflight)
          if (
            active.digest === digest ||
            (reason !== "token_inactive" && active.identity === identity)
          )
            active.reason = reason;
        for (const [k, entry] of this.cache) {
          if (
            entry.digest === digest ||
            (reason !== "token_inactive" && entry.identity === identity)
          )
            this.cache.delete(k);
        }
        await this.bounded(
          () =>
            this.gate.invalidate(
              {
                issuer: token.issuer,
                subject: token.subject,
                context,
                tokenDigest: digest,
                reason,
              },
              bounded,
            ),
          bounded,
          true,
        );
        throw new HeimdallError("unauthenticated");
      };
      const secret = await this.bounded(() => this.secret(bounded), bounded);
      if (typeof secret !== "string" || !secret.length || Buffer.byteLength(secret) > 4096)
        throw new HeimdallError("unavailable");
      const formPart = (value: string) => new URLSearchParams({ v: value }).toString().slice(2);
      const introspection = await this.json(
        "/oauth/v2/introspect",
        {
          method: "POST",
          headers: {
            "content-type": "application/x-www-form-urlencoded",
            authorization:
              "Basic " +
              Buffer.from(formPart(this.config.clientId) + ":" + formPart(secret)).toString(
                "base64",
              ),
          },
          body: new URLSearchParams({ token: raw, token_type_hint: "access_token" }),
        },
        bounded,
      );
      if (introspection.status !== 200 || typeof introspection.body?.active !== "boolean")
        throw new HeimdallError("unavailable");
      if (!introspection.body.active) return deny("token_inactive");
      const body = introspection.body;
      const audiences = typeof body.aud === "string" ? [body.aud] : body.aud;
      if (
        body.iss !== token.issuer ||
        body.sub !== token.subject ||
        !Array.isArray(audiences) ||
        !audiences.every((v) => typeof v === "string") ||
        !audiences.includes(this.config.audience) ||
        !Number.isSafeInteger(body.exp) ||
        (body.exp as number) < 0 ||
        (body.exp as number) * 1000 > token.expiresAt
      )
        throw new HeimdallError("unavailable");
      const expiresAt = (body.exp as number) * 1000;
      if (expiresAt <= this.now()) return deny("token_inactive");
      const lookup = await this.json(
        "/v2/users/" + encodeURIComponent(token.subject),
        { headers: { authorization: "Bearer " + raw } },
        bounded,
      );
      if (lookup.status === 401 || lookup.status === 404) return deny("token_inactive");
      if (lookup.status !== 200) throw new HeimdallError("unavailable");
      const user = lookup.body?.user as Record<string, unknown> | undefined;
      if (!user || typeof user !== "object" || user.userId !== token.subject)
        throw new HeimdallError("unavailable");
      if (
        !user.human ||
        typeof user.human !== "object" ||
        Array.isArray(user.human) ||
        user.machine !== undefined
      )
        return deny("token_inactive");
      if (user.state === "USER_STATE_DELETED") return deny("identity_deleted");
      if (user.state === "USER_STATE_INACTIVE" || user.state === "USER_STATE_LOCKED")
        return deny("identity_inactive");
      if (user.state !== "USER_STATE_ACTIVE") throw new HeimdallError("unavailable");
      if (await this.localDenied(token, bounded)) throw new HeimdallError("unauthenticated");
      checkCancellation(bounded);
      if (this.now() < started) throw new HeimdallError("unavailable");
      if (this.now() >= expiresAt) return deny("token_inactive");
      if (observation.reason) return deny(observation.reason);
      const principal = Object.freeze({
        issuer: token.issuer,
        subject: token.subject,
        kind: "human" as const,
        productId: this.config.productId,
        environment: this.config.environment,
        policyVersion: this.config.policyVersion,
        identityValidatedAt: started,
        expiresAt,
      });
      this.principals.add(principal);
      const until = Math.min(started + this.freshness, expiresAt);
      if (this.now() < until) {
        if (this.cache.size >= 1024) this.cache.delete(this.cache.keys().next().value!);
        this.cache.set(key, { principal, identity, digest, until });
      }
      return principal;
    } finally {
      this.inflight.delete(observation);
    }
  }
}
