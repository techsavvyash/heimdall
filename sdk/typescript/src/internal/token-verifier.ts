import { createLocalJWKSet, decodeProtectedHeader, jwtVerify } from "jose";
import type { JSONWebKeySet } from "jose";
import { validateEndpoint } from "./endpoints.js";
import { encodeIdentity } from "./identity-encoding.js";
import { checkCancellation, HeimdallError } from "./errors.js";

type Config = {
  issuer: string;
  audience: string;
  jwksUri: string;
  profile: "production" | "loopback";
};
type Token = Readonly<{ issuer: string; subject: string; expiresAt: number }>;
const cacheMs = 300000;
const cooldownMs = 30000;
const maxBytes = 262144;
// Signature verification alone does not establish current identity status or tenant access.
export class TokenVerifier {
  private readonly config: Config;
  private keys?: ReturnType<typeof createLocalJWKSet>;
  private kids = new Set<string>();
  private fetchedAt = -Infinity;
  private attemptedAt = -Infinity;
  private failed = false;
  private pending: Promise<void> | undefined;
  private readonly verified = new WeakSet<object>();
  constructor(
    config: Config,
    private readonly now: () => number = Date.now,
  ) {
    validateEndpoint(config.issuer, config.profile);
    validateEndpoint(config.jwksUri, config.profile);
    if (
      new URL(config.issuer).origin !== new URL(config.jwksUri).origin ||
      typeof config.audience !== "string" ||
      !config.audience ||
      Buffer.byteLength(config.audience) > 1024
    )
      throw new HeimdallError("invalid_argument");
    this.config = { ...config };
  }
  owns(value: unknown): boolean {
    return typeof value === "object" && value !== null && this.verified.has(value);
  }
  private async refresh() {
    this.attemptedAt = this.now();
    try {
      const response = await fetch(this.config.jwksUri, {
        redirect: "error",
        signal: AbortSignal.timeout(5000),
        headers: { accept: "application/json" },
      });
      if (response.status !== 200 || !response.body) {
        await response.body?.cancel();
        throw new Error();
      }
      const reader = response.body.getReader();
      const parts: Uint8Array[] = [];
      let size = 0;
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          size += value.length;
          if (size > maxBytes) throw new Error();
          parts.push(value);
        }
      } finally {
        await reader.cancel();
      }
      const jwks = JSON.parse(Buffer.concat(parts).toString("utf8")) as JSONWebKeySet;
      if (!Array.isArray(jwks.keys) || !jwks.keys.length || jwks.keys.length > 16)
        throw new Error();
      const kids = new Set<string>();
      for (const key of jwks.keys) {
        if (
          !key ||
          key.kty !== "RSA" ||
          typeof key.kid !== "string" ||
          !/^[\x21-\x7e]{1,128}$/.test(key.kid) ||
          kids.has(key.kid) ||
          (key.alg !== undefined && key.alg !== "RS256") ||
          (key.use !== undefined && key.use !== "sig") ||
          (key.key_ops !== undefined &&
            (!Array.isArray(key.key_ops) ||
              key.key_ops.length !== 1 ||
              key.key_ops[0] !== "verify")) ||
          ["d", "p", "q", "dp", "dq", "qi", "oth"].some((k) => k in key)
        )
          throw new Error();
        for (const field of [key.n, key.e]) {
          if (
            typeof field !== "string" ||
            !/^[A-Za-z0-9_-]+$/.test(field) ||
            Buffer.from(field, "base64url").toString("base64url") !== field ||
            Buffer.from(field, "base64url")[0] === 0
          )
            throw new Error();
        }
        const modulus = Buffer.from(key.n!, "base64url");
        const exponent = Buffer.from(key.e!, "base64url");
        const bits = (modulus.length - 1) * 8 + (32 - Math.clz32(modulus[0]!));
        const e = exponent.length <= 4 ? Number(BigInt("0x" + exponent.toString("hex"))) : 0;
        if (
          bits < 2048 ||
          bits > 8192 ||
          !(modulus.at(-1)! & 1) ||
          e < 3 ||
          e > 2147483647 ||
          !(e & 1)
        )
          throw new Error();
        kids.add(key.kid);
      }
      this.keys = createLocalJWKSet(jwks);
      this.kids = kids;
      this.fetchedAt = this.attemptedAt;
      this.failed = false;
    } catch {
      this.failed = true;
      throw new HeimdallError("unavailable");
    }
  }
  private async keyset(kid: string, signal: AbortSignal) {
    if (this.now() >= this.fetchedAt && this.now() - this.fetchedAt < cacheMs && this.kids.has(kid))
      return this.keys!;
    if (!this.pending && this.now() - this.attemptedAt >= cooldownMs) {
      this.pending = this.refresh().finally(() => {
        this.pending = undefined;
      });
    }
    if (this.pending) {
      let onAbort: (() => void) | undefined;
      try {
        await Promise.race([
          this.pending,
          new Promise<never>((_resolve, reject) => {
            onAbort = () => {
              try {
                checkCancellation(signal);
              } catch (e) {
                reject(e);
              }
            };
            signal.addEventListener("abort", onAbort, { once: true });
            if (signal.aborted) onAbort();
          }),
        ]);
      } finally {
        if (onAbort) signal.removeEventListener("abort", onAbort);
      }
    }
    checkCancellation(signal);
    if (this.now() < this.fetchedAt || this.now() - this.fetchedAt >= cacheMs || this.failed)
      throw new HeimdallError("unavailable");
    if (!this.kids.has(kid)) throw new HeimdallError("unauthenticated");
    return this.keys!;
  }
  async verify(token: string, signal?: AbortSignal): Promise<Token> {
    const bounded = AbortSignal.any([AbortSignal.timeout(5000), ...(signal ? [signal] : [])]);
    checkCancellation(bounded);
    if (
      typeof token !== "string" ||
      token.length > 16384 ||
      !/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(token)
    )
      throw new HeimdallError("unauthenticated");
    let kid: string;
    try {
      const header = decodeProtectedHeader(token);
      if (
        header.alg !== "RS256" ||
        typeof header.kid !== "string" ||
        !/^[\x21-\x7e]{1,128}$/.test(header.kid) ||
        header.crit !== undefined ||
        header.b64 !== undefined
      )
        throw new Error();
      kid = header.kid;
    } catch {
      throw new HeimdallError("unauthenticated");
    }
    const keys = await this.keyset(kid, bounded);
    try {
      const { payload } = await jwtVerify(token, keys, {
        algorithms: ["RS256"],
        issuer: this.config.issuer,
        audience: this.config.audience,
        requiredClaims: ["sub", "exp", "iat"],
        clockTolerance: 0,
        currentDate: new Date(this.now()),
      });
      const now = Math.floor(this.now() / 1000);
      if (
        typeof payload.sub !== "string" ||
        !Number.isSafeInteger(payload.exp) ||
        payload.exp! <= now ||
        payload.exp! > Math.floor(Number.MAX_SAFE_INTEGER / 1000) ||
        !Number.isSafeInteger(payload.iat) ||
        payload.iat! < 0 ||
        payload.iat! > now ||
        payload.iat! >= payload.exp! ||
        (payload.nbf !== undefined && (!Number.isSafeInteger(payload.nbf) || payload.nbf < 0))
      )
        throw new Error();
      encodeIdentity(this.config.issuer, payload.sub);
      checkCancellation(bounded);
      const result = Object.freeze({
        issuer: this.config.issuer,
        subject: payload.sub,
        expiresAt: payload.exp! * 1000,
      });
      this.verified.add(result);
      return result;
    } catch {
      checkCancellation(bounded);
      throw new HeimdallError("unauthenticated");
    }
  }
}
