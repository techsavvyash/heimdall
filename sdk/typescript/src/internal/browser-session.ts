import { createHash, randomBytes } from "node:crypto";
import type { AuditedAuthentication } from "./audited-authentication.js";
import type { createBrowserLoginHTTP } from "./browser-login-http.js";
import { checkCancellation, HeimdallError } from "./errors.js";
import { sessionIdentityContext } from "./identity-context.js";
import { sessionStatus } from "./session-contract.js";
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
  store: Pick<
    SessionRecords,
    "createSession" | "getSession" | "compareAndSwapSession" | "deleteSession"
  >,
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

  const attributes = `Path=/; HttpOnly; SameSite=Lax${secure ? "; Secure" : ""}`;
  const sessionKey = (request: Request) => {
    const header = request.headers.get("cookie") ?? "";
    if (header.length > 16384) throw new HeimdallError("unauthenticated");
    const values = header
      .split(";")
      .map((item) => item.trim())
      .filter((item) => item.startsWith(cookie + "="));
    if (values.length === 0) return null;
    const opaque = values[0]!.slice(cookie.length + 1);
    if (
      values.length !== 1 ||
      !/^[A-Za-z0-9_-]{43}(?![\s\S])/.test(opaque) ||
      Buffer.from(opaque, "base64url").toString("base64url") !== opaque
    )
      throw new HeimdallError("unauthenticated");
    return createHash("sha256").update(opaque, "ascii").digest("base64url");
  };

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
        `${cookie}=${opaque}; Max-Age=${Math.floor(absoluteLifetimeMs / 1000)}; ${attributes}`,
      );
      return new Response(null, { status: 303, headers });
    },
    async authenticate(request: Request) {
      checkCancellation(request.signal);
      if (new URL(request.url).origin !== login.scope.origin)
        throw new HeimdallError("unauthenticated");
      const key = sessionKey(request);
      if (!key) throw new HeimdallError("unauthenticated");
      let stored = await store.getSession(key, request.signal);
      if (!stored) throw new HeimdallError("unauthenticated");
      let payload = readSession(stored.value, stored.expiresAt);
      const status = sessionStatus(
        payload,
        { ...payload, productId, environment, issuer: login.scope.issuer },
        now(),
        0,
      );
      if (status === "refreshing") throw new HeimdallError("unavailable");
      if (status === "reauth_required") throw new HeimdallError("unauthenticated");
      let refreshDeadline = Infinity;
      let operation = request.signal;
      if (status === "refresh_required") {
        refreshDeadline = Math.min(payload.idleExpiresAt, now() + 15_000);
        const pending = {
          ...payload,
          state: "refreshing",
          refreshAttempt: { id: randomBytes(16).toString("hex"), expiresAt: refreshDeadline },
        } as const;
        const claimed = await store.compareAndSwapSession(
          key,
          stored.revision,
          Buffer.from(JSON.stringify(pending)),
          payload.idleExpiresAt,
          request.signal,
        );
        if (!claimed) throw new HeimdallError("unavailable");
        stored = claimed;
        operation = AbortSignal.any([
          request.signal,
          AbortSignal.timeout(Math.max(0, refreshDeadline - now())),
        ]);
        if (now() >= refreshDeadline) throw new HeimdallError("unauthenticated");
        const tokens = await login.refresh(payload.refreshToken!, operation);
        payload = { ...payload, ...tokens, accessTokenExpiresAt: Number.MAX_SAFE_INTEGER };
      }
      const actor = await auth.verify(
        payload.accessToken,
        sessionIdentityContext(key, stored.revision),
        operation,
      );
      if (
        actor.productId !== productId ||
        actor.environment !== environment ||
        actor.issuer !== payload.issuer ||
        actor.subject !== payload.subject ||
        actor.policyVersion !== payload.policyVersion
      )
        throw new HeimdallError("unauthenticated");
      const at = now();
      const expires = Math.min(payload.absoluteExpiresAt, at + idleLifetimeMs);
      const accessExpires = Math.min(payload.accessTokenExpiresAt, actor.expiresAt);
      if (
        at >= Math.min(payload.idleExpiresAt, accessExpires, refreshDeadline) ||
        at < Math.max(payload.lastActivityAt, actor.identityValidatedAt)
      )
        throw new HeimdallError("unauthenticated");
      const updated = await store.compareAndSwapSession(
        key,
        stored.revision,
        Buffer.from(
          JSON.stringify({
            ...payload,
            lastActivityAt: at,
            idleExpiresAt: expires,
            accessTokenExpiresAt: accessExpires,
            identityValidatedAt: actor.identityValidatedAt,
          }),
        ),
        expires,
        operation,
      );
      checkCancellation(request.signal);
      if (!updated) {
        if (await store.getSession(key, request.signal)) throw new HeimdallError("unavailable");
        throw new HeimdallError("unauthenticated");
      }
      if (now() >= Math.min(expires, accessExpires, refreshDeadline))
        throw new HeimdallError("unauthenticated");
      return actor;
    },

    async logout(request: Request): Promise<Response> {
      checkCancellation(request.signal);
      if (
        request.method !== "POST" ||
        new URL(request.url).origin !== login.scope.origin ||
        request.headers.get("origin") !== login.scope.origin
      )
        throw new HeimdallError("forbidden");
      const key = sessionKey(request);
      if (key) await store.deleteSession(key, request.signal);
      checkCancellation(request.signal);
      return new Response(null, {
        status: 204,
        headers: {
          "cache-control": "no-store",
          "set-cookie": `${cookie}=; Max-Age=0; ${attributes}`,
        },
      });
    },
  };
}

type SessionPayload = Parameters<typeof sessionStatus>[0] & {
  accessToken: string;
  schemaVersion: 1;
};
export function readSession(bytes: Uint8Array, expires?: number): SessionPayload {
  try {
    const payload: SessionPayload = JSON.parse(
      new TextDecoder("utf-8", { fatal: true }).decode(bytes),
    );
    const instants = [
      payload.createdAt,
      payload.lastActivityAt,
      payload.absoluteExpiresAt,
      payload.idleExpiresAt,
      payload.accessTokenExpiresAt,
      payload.identityValidatedAt,
    ];
    if (
      payload.schemaVersion !== 1 ||
      (expires !== undefined && payload.idleExpiresAt !== expires) ||
      !instants.every((at) => Number.isSafeInteger(at) && at >= 0) ||
      ![
        payload.productId,
        payload.environment,
        payload.issuer,
        payload.subject,
        payload.policyVersion,
        payload.accessToken,
      ].every((field) => typeof field === "string" && field.length > 0) ||
      (Object.hasOwn(payload, "refreshToken") &&
        (typeof payload.refreshToken !== "string" || !payload.refreshToken)) ||
      !["active", "refreshing", "reauth_required"].includes(payload.state) ||
      (payload.state === "refreshing"
        ? !payload.refreshAttempt ||
          typeof payload.refreshAttempt.id !== "string" ||
          !Number.isSafeInteger(payload.refreshAttempt.expiresAt)
        : Object.hasOwn(payload, "refreshAttempt"))
    )
      throw new Error();
    return payload;
  } catch {
    throw new HeimdallError("unavailable");
  }
}
