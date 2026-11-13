import { encodeIdentity } from "./internal/identity-encoding.js";
import { browserSession } from "./internal/browser-session.js";
import { createBrowserLoginHTTP } from "./internal/browser-login-http.js";
import { parseDeployment } from "./internal/configuration.js";
import { discoverOIDC } from "./internal/oidc-discovery.js";
import { PinnedModel } from "./internal/model-binding.js";
import { AuditedAuthentication } from "./internal/audited-authentication.js";
import { bounded, known } from "./internal/audited-decision.js";
import { HeimdallError, checkCancellation } from "./internal/errors.js";

type AuthenticationArguments = ConstructorParameters<typeof AuditedAuthentication>;
export type IdentityGate = AuthenticationArguments[2];
export type AuditAppend = AuthenticationArguments[3];
export type AuditReceipt = Awaited<ReturnType<AuditAppend>>;
export type RuntimeStorage = {
  productId: string;
  environment: string;
  /** Read-only readiness: validate durable stores and their configured scope. Never migrate here. */
  ready(signal: AbortSignal): Promise<void>;
  identity: IdentityGate;
  appendAudit: AuditAppend;
  browser?: {
    login: Parameters<typeof createBrowserLoginHTTP>[1];
    sessions: Parameters<typeof browserSession>[2];
  };
};
export type RuntimeOptions = {
  policyVersion: string;
  identityFreshnessMs?: number;
  credentials: {
    clientSecret(signal: AbortSignal): Promise<string>;
    openfgaKey(signal: AbortSignal): Promise<string>;
  };
  storage: RuntimeStorage;
};
declare const principalBrand: unique symbol;
export type Principal = Awaited<ReturnType<AuditedAuthentication["verify"]>> & {
  readonly [principalBrand]: true;
};
export type SessionOptions = Parameters<typeof browserSession>[3] & {
  callbackUrl: string;
  zitadelAPIAudience: string;
};
export type Sessions = Readonly<{
  start(request: Request): Promise<Response>;
  callback(request: Request): Promise<Response>;
  authenticate(request: Request): Promise<Principal>;
  logout(request: Request): Promise<Response>;
}>;
export type Tenant = Readonly<{
  roles(actor: Principal, options: { signal: AbortSignal }): Promise<string[]>;
  can(actor: Principal, permission: string, options: { signal: AbortSignal }): Promise<boolean>;
  require(actor: Principal, permission: string, options: { signal: AbortSignal }): Promise<void>;
}>;
export type Heimdall = Readonly<{
  tenant(id: string): Tenant;
  createSessions(options: SessionOptions, request: { signal: AbortSignal }): Promise<Sessions>;
  tokens: Readonly<{
    verify(accessToken: string, options: { signal: AbortSignal }): Promise<Principal>;
  }>;
}>;

/** Construct a scoped core runtime. Storage ports are trusted durable product integrations. */
export async function createHeimdall(
  configJSON: string,
  manifestJSON: string,
  options: RuntimeOptions,
  request: { signal: AbortSignal },
): Promise<Heimdall> {
  if (!(request?.signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
  const signal = AbortSignal.any([request.signal, AbortSignal.timeout(5000)]);
  checkCancellation(signal);
  const deployment = parseDeployment(configJSON, manifestJSON);
  const { config, manifest, artifact } = deployment;
  const storage = options?.storage;
  if (
    !options ||
    typeof options.policyVersion !== "string" ||
    !/^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(options.policyVersion) ||
    (options.identityFreshnessMs !== undefined &&
      (!Number.isSafeInteger(options.identityFreshnessMs) ||
        options.identityFreshnessMs < 0 ||
        options.identityFreshnessMs > 60000)) ||
    storage?.productId !== config.productId ||
    storage?.environment !== config.environment ||
    typeof storage?.ready !== "function" ||
    typeof storage?.appendAudit !== "function" ||
    typeof storage?.identity?.check !== "function" ||
    typeof storage?.identity?.invalidate !== "function" ||
    typeof options.credentials?.clientSecret !== "function" ||
    typeof options.credentials?.openfgaKey !== "function" ||
    !(request?.signal instanceof AbortSignal)
  )
    throw new HeimdallError("invalid_argument");
  // Capture all ports before the first await; caller mutation cannot redirect this runtime.
  const ready = storage.ready.bind(storage);
  const append = storage.appendAudit.bind(storage);
  const gate = {
    check: storage.identity.check.bind(storage.identity),
    invalidate: storage.identity.invalidate.bind(storage.identity),
  };
  const browser = storage.browser && {
    login: {
      start: storage.browser.login.start.bind(storage.browser.login),
      claim: storage.browser.login.claim.bind(storage.browser.login),
    },
    sessions: {
      createSession: storage.browser.sessions.createSession.bind(storage.browser.sessions),
      getSession: storage.browser.sessions.getSession.bind(storage.browser.sessions),
      compareAndSwapSession: storage.browser.sessions.compareAndSwapSession.bind(
        storage.browser.sessions,
      ),
      deleteSession: storage.browser.sessions.deleteSession.bind(storage.browser.sessions),
    },
  };
  const secret = options.credentials.clientSecret.bind(options.credentials);
  const key = options.credentials.openfgaKey.bind(options.credentials);
  const policyVersion = options.policyVersion;
  const freshnessMs = options.identityFreshnessMs ?? 60000;
  const profile = config.transportProfile ?? "production";
  try {
    await bounded(ready, signal);
    const metadata = await discoverOIDC(config.issuer, profile, signal);
    // The identity-status implementation uses the pinned ZITADEL introspection route.
    if (
      metadata.introspection_endpoint !==
      config.issuer.replace(/\/$/, "") + "/oauth/v2/introspect"
    )
      throw new HeimdallError("unavailable");
    const model = new PinnedModel({ ...config.openfga, profile }, manifest, config, artifact, key);
    await model.verify(signal);
    checkCancellation(signal);
    const authentication = new AuditedAuthentication(
      {
        issuer: config.issuer,
        audience: config.audience,
        jwksUri: metadata.jwks_uri,
        profile,
        productId: config.productId,
        environment: config.environment,
        clientId: config.clientId,
        policyVersion,
        freshnessMs,
      },
      secret,
      gate,
      append,
    );
    return Object.freeze({
      tenant(id: string) {
        if (typeof id !== "string" || !/^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(id))
          throw new HeimdallError("invalid_argument");
        const can: Tenant["can"] = async (actor, permission, options) => {
          if (
            !(options?.signal instanceof AbortSignal) ||
            typeof permission !== "string" ||
            !Object.hasOwn(artifact.permissions, permission)
          )
            throw new HeimdallError("invalid_argument");
          return authentication.checkPermission(
            actor,
            id,
            permission,
            config.openfga.modelId,
            config.manifestVersion,
            (signal) =>
              model.check(encodeIdentity(actor.issuer, actor.subject), id, permission, signal),
            options.signal,
          );
        };
        return Object.freeze({
          async roles(actor: Principal, options: { signal: AbortSignal }) {
            if (!(options?.signal instanceof AbortSignal))
              throw new HeimdallError("invalid_argument");
            const result: string[] = [];
            await authentication.checkPermission(
              actor,
              id,
              "roles.list",
              config.openfga.modelId,
              config.manifestVersion,
              async (operation, parent) => {
                const user = encodeIdentity(actor.issuer, actor.subject);
                for (const role of Object.keys(artifact.roles).sort()) {
                  const allowed = await authentication.checkPermission(
                    actor,
                    id,
                    "role." + role,
                    config.openfga.modelId,
                    config.manifestVersion,
                    (signal) => model.checkRole(user, id, role, signal),
                    operation,
                    parent,
                  );
                  if (allowed) result.push(role);
                }
                return result.length > 0;
              },
              options.signal,
            );
            return result;
          },
          can,
          async require(actor: Principal, permission: string, options: { signal: AbortSignal }) {
            if (!(await can(actor, permission, options))) throw new HeimdallError("forbidden");
          },
        });
      },
      async createSessions(options: SessionOptions, request: { signal: AbortSignal }) {
        if (
          !browser ||
          !(request?.signal instanceof AbortSignal) ||
          !manifest.clients[config.clientKey]?.redirectUris.includes(options?.callbackUrl) ||
          !options.zitadelAPIAudience
        )
          throw new HeimdallError("invalid_argument");
        const operation = AbortSignal.any([request.signal, AbortSignal.timeout(5000)]);
        const { callbackUrl, zitadelAPIAudience, ...sessionOptions } = options;
        const clientSecret = await bounded(secret, operation).catch((error: unknown) => {
          checkCancellation(operation);
          throw known(error);
        });
        const login = await createBrowserLoginHTTP(
          {
            issuer: config.issuer,
            audience: config.audience,
            clientId: config.clientId,
            profile,
            productId: config.productId,
            environment: config.environment,
            returnToKey: "default",
            clientSecret,
            redirectUri: callbackUrl,
            zitadelAPIAudience,
          },
          browser.login,
          operation,
        );
        const sessions = browserSession(login, authentication, browser.sessions, sessionOptions);
        return Object.freeze({
          ...sessions,
          authenticate: async (request: Request) =>
            (await sessions.authenticate(request)) as Principal,
        });
      },
      tokens: Object.freeze({
        verify: async (raw: string, call: { signal: AbortSignal }) => {
          if (!(call?.signal instanceof AbortSignal)) throw new HeimdallError("invalid_argument");
          return (await authentication.verify(raw, "bearer", call.signal)) as Principal;
        },
      }),
    });
  } catch (error) {
    checkCancellation(signal);
    throw known(error);
  }
}
