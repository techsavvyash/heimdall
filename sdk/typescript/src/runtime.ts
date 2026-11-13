import type { ManagedExport } from "./internal/postgres/export.js";
import type {
  MaintenanceContext,
  TenantSSOPolicySource,
} from "./internal/audited-authentication.js";
import { randomUUID } from "node:crypto";
import { managedMemberships, type Memberships } from "./internal/managed-memberships.js";
import type { ManagedWorkspace, WorkspaceRecords } from "./internal/postgres/workspaces.js";
import { managedOnboarding } from "./internal/managed-onboarding.js";
import type {
  WorkspaceCreationPolicy,
  WorkspaceOnboarding,
} from "./internal/managed-onboarding.js";
import type { OperationRecords } from "./internal/postgres/operations.js";
import { workspaceDiscovery } from "./internal/workspace-discovery.js";
import type {
  DiscoveryOptions,
  WorkspaceDiscovery,
  WorkspaceSource,
} from "./internal/workspace-discovery.js";
import { encodeIdentity } from "./internal/identity-encoding.js";
import { browserSession } from "./internal/browser-session.js";
import { createBrowserLoginHTTP } from "./internal/browser-login-http.js";
import { BrowserLoginRecords } from "./internal/postgres/login.js";
import { customSessionStore } from "./internal/custom-session-store.js";
import type { SessionStoreOptions } from "./internal/custom-session-store.js";
import { parseIdentityContext } from "./internal/identity-context.js";
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
  managed?: {
    allows(id: string, signal: AbortSignal): Promise<boolean>;
    source(actor: Principal): WorkspaceSource;
    workspace?: WorkspaceRecords["get"];
    exportReader?: Pick<ManagedExport, "scan" | "withSnapshot">;
    operations?: (binding: {
      productId: string;
      environment: string;
      modelId: string;
      storeId: string;
      modelHash: string;
      manifestVersion: number;
    }) => OperationRecords;
  };
  browser?: {
    login: Parameters<typeof createBrowserLoginHTTP>[1];
    sessions: Parameters<typeof browserSession>[2];
  };
};
export type RuntimeOptions = {
  policyVersion: string;
  tenantSSOPolicy?: TenantSSOPolicySource;
  authorizeWorkspaceCreation?: WorkspaceCreationPolicy;
  authorizeWorkspaceLifecycle?: WorkspaceLifecyclePolicy;
  authorizeManagedExport?: ManagedExportPolicy;
  identityFreshnessMs?: number;
  credentials: {
    clientSecret(signal: AbortSignal): Promise<string>;
    openfgaKey(signal: AbortSignal): Promise<string>;
  };
  storage: RuntimeStorage;
  sessionStorage?: SessionStoreOptions;
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
  startFederation(request: Request, tenantId: string): Promise<Response>;
  callback(request: Request): Promise<Response>;
  authenticate(request: Request): Promise<Principal>;
  logout(request: Request): Promise<Response>;
}>;
export type WorkspaceLifecyclePolicy = (
  actor: Principal,
  workspaceId: string,
  action: "suspend" | "resume",
  signal: AbortSignal,
) => Promise<boolean>;
type LifecycleChange = (actor: Principal, options: { signal: AbortSignal }) => Promise<void>;
export type Tenant = Readonly<{
  memberships?: Memberships;
  suspend?: LifecycleChange;
  resume?: LifecycleChange;
  roles(actor: Principal, options: { signal: AbortSignal }): Promise<string[]>;
  can(actor: Principal, permission: string, options: { signal: AbortSignal }): Promise<boolean>;
  require(actor: Principal, permission: string, options: { signal: AbortSignal }): Promise<void>;
}>;
export type ManagedExportPolicy = (
  scope: MaintenanceContext,
  signal: AbortSignal,
) => Promise<boolean>;
export type ManagedExportRequest = Parameters<AuditedAuthentication["runManagedExport"]>[1];
type ExportCall = (
  token: string,
  request: ManagedExportRequest,
  options: { signal: AbortSignal },
) => Promise<string>;

export type Heimdall = Readonly<{
  tenant(id: string): Tenant;
  onboarding?: WorkspaceOnboarding;
  reconcile?: WorkspaceOnboarding["reconcile"];
  operationMetrics?: WorkspaceOnboarding["operationMetrics"];
  exportManagedPage?: ExportCall;
  exportManagedInventory?: ExportCall;
  createWorkspaceDiscovery(options: DiscoveryOptions): WorkspaceDiscovery;
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
  if (storage.browser && options.sessionStorage) throw new HeimdallError("invalid_argument");
  const custom =
    options.sessionStorage &&
    customSessionStore(config.productId, config.environment, options.sessionStorage);
  const invalidate = storage.identity.invalidate.bind(storage.identity);
  const gate: IdentityGate = {
    check: storage.identity.check.bind(storage.identity),
    invalidate: async (negative, signal) => {
      const session = custom && parseIdentityContext(negative.context);
      if (session && negative.reason === "token_inactive") {
        await custom.invalidateSessionVersion(session.key, session.revision, negative, signal);
      }
      await invalidate(session ? { ...negative, context: "bearer" } : negative, signal);
    },
  };
  const managed = storage.managed && {
    allows: storage.managed.allows.bind(storage.managed),
    source: storage.managed.source.bind(storage.managed),
  };
  const workspace = storage.managed?.workspace?.bind(storage.managed);
  const browser = custom
    ? { login: new BrowserLoginRecords(custom), sessions: custom }
    : storage.browser && {
        login: {
          start: storage.browser.login.start.bind(storage.browser.login),
          claim: storage.browser.login.claim.bind(storage.browser.login),
          ...(storage.browser.login.startFederation && storage.browser.login.claimFederation
            ? {
                startFederation: storage.browser.login.startFederation.bind(storage.browser.login),
                claimFederation: storage.browser.login.claimFederation.bind(storage.browser.login),
              }
            : {}),
        },
        sessions: {
          createSession: storage.browser.sessions.createSession.bind(storage.browser.sessions),
          getSession: storage.browser.sessions.getSession.bind(storage.browser.sessions),
          compareAndSwapSession: storage.browser.sessions.compareAndSwapSession.bind(
            storage.browser.sessions,
          ),
          deleteSession: storage.browser.sessions.deleteSession.bind(storage.browser.sessions),
          ...(storage.browser.sessions.replaceSession
            ? {
                replaceSession: storage.browser.sessions.replaceSession.bind(
                  storage.browser.sessions,
                ),
              }
            : {}),
        },
      };
  const secret = options.credentials.clientSecret.bind(options.credentials);
  const key = options.credentials.openfgaKey.bind(options.credentials);
  const policyVersion = options.policyVersion;
  const tenantSSOPolicy = options.tenantSSOPolicy;
  if (tenantSSOPolicy !== undefined && typeof tenantSSOPolicy !== "function")
    throw new HeimdallError("invalid_argument");
  const authorize = options.authorizeWorkspaceCreation;
  const authorizeLifecycle = options.authorizeWorkspaceLifecycle;
  const operations = storage.managed?.operations?.({
    productId: config.productId,
    environment: config.environment,
    storeId: config.openfga.storeId,
    modelId: config.openfga.modelId,
    modelHash: artifact.modelHash,
    manifestVersion: config.manifestVersion,
  });
  if (authorize !== undefined && (!operations || typeof authorize !== "function"))
    throw new HeimdallError("invalid_argument");
  if (
    authorizeLifecycle !== undefined &&
    (!operations || !workspace || typeof authorizeLifecycle !== "function")
  )
    throw new HeimdallError("invalid_argument");
  const authorizeExport = options.authorizeManagedExport;
  const exportReader = storage.managed?.exportReader;
  if (
    authorizeExport !== undefined &&
    (typeof authorizeExport !== "function" ||
      typeof exportReader?.scan !== "function" ||
      typeof exportReader?.withSnapshot !== "function")
  )
    throw new HeimdallError("invalid_argument");
  const scanExport = exportReader?.scan.bind(exportReader);
  const snapshotExport = exportReader?.withSnapshot.bind(exportReader);
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
      Date.now,
      tenantSSOPolicy,
    );
    const onboarding =
      operations &&
      managedOnboarding(
        configJSON,
        manifestJSON,
        config,
        policyVersion,
        operations,
        authentication,
        key,
        gate,
        authorize,
      );
    const exportManagedPage: ExportCall = (token, request, call) => {
      if (!(call?.signal instanceof AbortSignal))
        return Promise.reject(new HeimdallError("invalid_argument"));
      return authentication.runManagedExport(
        token,
        request,
        config.openfga.modelId,
        config.manifestVersion,
        authorizeExport!,
        scanExport!,
        call.signal,
      );
    };
    const exportManagedInventory: ExportCall = (token, request, call) => {
      if (!(call?.signal instanceof AbortSignal))
        return Promise.reject(new HeimdallError("invalid_argument"));
      return authentication.runManagedInventory(
        token,
        request,
        config.openfga.modelId,
        config.manifestVersion,
        authorizeExport!,
        snapshotExport!,
        call.signal,
      );
    };
    return Object.freeze({
      ...(onboarding
        ? {
            onboarding,
            reconcile: onboarding.reconcile,
            operationMetrics: onboarding.operationMetrics,
          }
        : {}),
      ...(authorizeExport ? { exportManagedPage, exportManagedInventory } : {}),
      createWorkspaceDiscovery(options: DiscoveryOptions) {
        return workspaceDiscovery(
          options,
          {
            productId: config.productId,
            environment: config.environment,
            modelId: config.openfga.modelId,
            manifestVersion: config.manifestVersion,
          },
          authentication,
          model,
          managed,
        );
      },
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
            async (signal) =>
              (!managed || (await managed.allows(id, signal))) &&
              model.check(encodeIdentity(actor.issuer, actor.subject), id, permission, signal),
            options.signal,
          );
        };
        const memberships =
          operations &&
          workspace &&
          managedMemberships(
            id,
            config,
            manifest.ownerRole,
            policyVersion,
            operations,
            { get: workspace },
            authentication,
            model,
            gate,
          );
        const setLifecycle = async (
          actor: Principal,
          action: "suspend" | "resume",
          call: { signal: AbortSignal },
        ) => {
          if (!operations || !workspace || !(call?.signal instanceof AbortSignal))
            throw new HeimdallError("invalid_argument");
          const signal = AbortSignal.any([call.signal, AbortSignal.timeout(30_000)]);
          checkCancellation(signal);
          const observed: { workspace?: ManagedWorkspace | null } = {};
          const allowed = await authentication.checkPermission(
            actor,
            id,
            "workspace." + action,
            config.openfga.modelId,
            config.manifestVersion,
            async (operation) => {
              if (!authorizeLifecycle) return false;
              observed.workspace = await workspace(id, operation);
              return (
                (await bounded((s) => authorizeLifecycle(actor, id, action, s), operation)) === true
              );
            },
            signal,
          );
          if (!allowed) throw new HeimdallError("forbidden");
          const record = observed.workspace;
          if (!record) throw new HeimdallError("not_found");
          if (
            !["active", "suspended"].includes(record.lifecycle) ||
            (action === "resume" && record.mutationBlocked)
          )
            throw new HeimdallError("conflict");
          await operations.setWorkspaceLifecycle(
            record,
            action === "suspend" ? "suspended" : "active",
            actor,
            {
              eventId: randomUUID(),
              requestId: randomUUID(),
              policyVersion,
              recordedAt: Math.max(Date.now(), record.updatedAt),
            },
            signal,
          );
        };
        return Object.freeze({
          ...(memberships
            ? {
                memberships,
                suspend: (actor: Principal, call: { signal: AbortSignal }) =>
                  setLifecycle(actor, "suspend", call),
                resume: (actor: Principal, call: { signal: AbortSignal }) =>
                  setLifecycle(actor, "resume", call),
              }
            : {}),
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
                if (managed && !(await managed.allows(id, operation))) return false;
                const user = encodeIdentity(actor.issuer, actor.subject);
                for (const role of Object.keys(artifact.roles).sort()) {
                  const allowed = await authentication.checkPermission(
                    actor,
                    id,
                    "role." + role,
                    config.openfga.modelId,
                    config.manifestVersion,
                    async (signal) =>
                      (!managed || (await managed.allows(id, signal))) &&
                      model.checkRole(user, id, role, signal),
                    operation,
                    parent,
                  );
                  if (allowed) result.push(role);
                }
                if (managed && result.length && !(await managed.allows(id, operation)))
                  result.length = 0;
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
        const sessions = browserSession(
          login,
          authentication,
          browser.sessions,
          sessionOptions,
          Date.now,
          tenantSSOPolicy,
        );
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
