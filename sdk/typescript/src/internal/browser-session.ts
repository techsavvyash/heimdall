import { createHash, randomBytes } from "node:crypto";
import type { AuditedAuthentication } from "./audited-authentication.js";
import type { createBrowserLoginHTTP } from "./browser-login-http.js";
import { checkCancellation, HeimdallError } from "./errors.js";
import type { SessionRecords } from "./postgres/sessions.js";

type Login = Awaited<ReturnType<typeof createBrowserLoginHTTP>>;
type Options = {
  returnTo: string;
  absoluteLifetimeMs?: number;
  idleLifetimeMs?: number;
};

export function browserSession(
  login: Login,
  auth: Pick<AuditedAuthentication, "verify">,
  store: Pick<SessionRecords, "createSession">,
  options: Options,
  now: () => number = Date.now,
) {
  const { returnTo, absoluteLifetimeMs = 28_800_000, idleLifetimeMs = 1_800_000 } = options;
  if (
    !/^\/(?:[A-Za-z0-9_-]+\/)*[A-Za-z0-9_-]*(?![\s\S])/.test(returnTo) ||
    ![absoluteLifetimeMs, idleLifetimeMs].every((ms) => Number.isSafeInteger(ms) && ms >= 1000) ||
    idleLifetimeMs > absoluteLifetimeMs
  )
    throw new HeimdallError("invalid_argument");
  const { productId, environment, returnToKey, secure } = login.scope;
  const cookie = `${secure ? "__Host-" : ""}heimdall-${productId.length}-${productId}-${environment}-session`;

  return {
    start: login.start,
    async callback(request: Request): Promise<Response> {
      const { tokens, clearBindingCookie, returnToKey: actualKey } = await login.callback(request);
      if (actualKey !== returnToKey) throw new HeimdallError("unauthenticated");
      const actor = await auth.verify(tokens.accessToken, "bearer", request.signal);
      if (
        actor.issuer !== tokens.identity.issuer ||
        actor.subject !== tokens.identity.subject ||
        actor.productId !== productId ||
        actor.environment !== environment
      )
        throw new HeimdallError("unauthenticated");
      checkCancellation(request.signal);
      const issuedAt = now();
      const absoluteExpiresAt = issuedAt + absoluteLifetimeMs;
      const idleExpiresAt = issuedAt + idleLifetimeMs;
      if (!Number.isSafeInteger(absoluteExpiresAt)) throw new HeimdallError("invalid_argument");
      if (
        issuedAt >= Math.min(actor.expiresAt, tokens.accessExpiresAt) ||
        issuedAt < actor.identityValidatedAt
      )
        throw new HeimdallError("unauthenticated");
      const opaque = randomBytes(32).toString("base64url");
      const key = createHash("sha256").update(opaque, "ascii").digest("base64url");
      const payload = {
        schemaVersion: 1,
        productId,
        environment,
        issuer: actor.issuer,
        subject: actor.subject,
        policyVersion: actor.policyVersion,
        createdAt: actor.identityValidatedAt,
        lastActivityAt: issuedAt,
        absoluteExpiresAt,
        idleExpiresAt,
        accessTokenExpiresAt: Math.min(actor.expiresAt, tokens.accessExpiresAt),
        identityValidatedAt: actor.identityValidatedAt,
        state: "active",
        accessToken: tokens.accessToken,
        ...(tokens.refreshToken === undefined ? {} : { refreshToken: tokens.refreshToken }),
      };
      const stored = await store.createSession(
        key,
        Buffer.from(JSON.stringify(payload)),
        idleExpiresAt,
        request.signal,
      );
      checkCancellation(request.signal);
      if (!stored || now() >= Math.min(idleExpiresAt, payload.accessTokenExpiresAt))
        throw new HeimdallError("unauthenticated");
      const headers = new Headers({
        location: returnTo,
        "cache-control": "no-store",
        "referrer-policy": "no-referrer",
      });
      headers.append("set-cookie", clearBindingCookie);
      headers.append(
        "set-cookie",
        `${cookie}=${opaque}; Max-Age=${Math.floor(absoluteLifetimeMs / 1000)}; Path=/; HttpOnly; SameSite=Lax${secure ? "; Secure" : ""}`,
      );
      return new Response(null, { status: 303, headers });
    },
  };
}
