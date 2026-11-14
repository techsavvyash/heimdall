import type { Pool } from "pg";
import { validateEndpoint, validClientId } from "../internal/endpoints.js";
import { bounded } from "../internal/audited-decision.js";
import { HeimdallError } from "../internal/errors.js";
import { setupJournal } from "./journal.js";
import { providerId, zitadel } from "./zitadel.js";

const service = "/zitadel.application.v2.ApplicationService/";
const web = "OIDC_APP_TYPE_WEB";
const basic = "OIDC_AUTH_METHOD_TYPE_BASIC";
const jwt = "OIDC_TOKEN_TYPE_JWT";
const responseTypes = ["OIDC_RESPONSE_TYPE_CODE"];
const grantTypes = ["OIDC_GRANT_TYPE_AUTHORIZATION_CODE", "OIDC_GRANT_TYPE_REFRESH_TOKEN"];

type Options = {
  scope: Parameters<typeof setupJournal>[1];
  ordinal: number;
  issuer: string;
  token: string;
  projectId: string;
  name: string;
  redirectUris: readonly string[];
  transportProfile?: "production" | "loopback";
  existingApplicationId?: string | undefined;
  saveSecret(
    applicationId: string,
    clientId: string,
    secret: string,
    signal: AbortSignal,
  ): Promise<void>;
  hasSecret(applicationId: string, clientId: string, signal: AbortSignal): Promise<boolean>;
};

export async function provisionApplication(pool: Pool, options: Options, signal: AbortSignal) {
  const { scope, ordinal, issuer, token, projectId, name, redirectUris, existingApplicationId } =
    options;
  const profile = options.transportProfile ?? "production";
  const call = zitadel(issuer, token, profile, signal);
  if (
    !providerId(projectId) ||
    !providerId(name) ||
    (existingApplicationId !== undefined && !providerId(existingApplicationId)) ||
    !Number.isInteger(ordinal) ||
    ordinal < 2 ||
    ordinal > scope.stepCount ||
    !Array.isArray(redirectUris) ||
    redirectUris.length < 1 ||
    redirectUris.length > 32 ||
    new Set(redirectUris).size !== redirectUris.length ||
    typeof options.saveSecret !== "function" ||
    typeof options.hasSecret !== "function"
  )
    throw new HeimdallError("invalid_argument");
  const redirects = [...redirectUris];
  for (const uri of redirects) {
    validateEndpoint(uri, profile);
    if (!/^https?:\/\/[^/]+\//.test(uri)) throw new HeimdallError("invalid_argument");
  }

  const journal = setupJournal(pool, scope);
  const step = await journal.claim(ordinal, signal);
  const applicationId = existingApplicationId ?? step.dispatchId;
  const unknown = () => new HeimdallError("outcome_unknown", step.dispatchId);
  const conflict = () => new HeimdallError("conflict", step.dispatchId);
  if (step.state === "confirmed" && step.resourceId !== applicationId) throw conflict();
  let createdClientId: string | undefined;
  if (step.state === "claimed" && !existingApplicationId) {
    try {
      const created = await call<{
        applicationId: string;
        oidcConfiguration?: { clientId: string; clientSecret: string };
      }>(service + "CreateApplication", {
        projectId,
        applicationId,
        name,
        oidcConfiguration: {
          redirectUris: redirects,
          responseTypes,
          grantTypes,
          applicationType: web,
          authMethodType: basic,
          accessTokenType: jwt,
        },
      });
      const oidc = created?.oidcConfiguration;
      if (
        created?.applicationId !== applicationId ||
        !oidc ||
        !validClientId(oidc.clientId) ||
        typeof oidc.clientSecret !== "string" ||
        !oidc.clientSecret ||
        oidc.clientSecret.length > 4096
      )
        throw unknown();
      createdClientId = oidc.clientId;
      await bounded(
        (operation) =>
          options.saveSecret(applicationId, oidc.clientId, oidc.clientSecret, operation),
        AbortSignal.any([signal, AbortSignal.timeout(5000)]),
      );
    } catch {
      throw unknown();
    }
  }
  const observed = (
    await call<{ application?: Record<string, unknown> }>(service + "GetApplication", {
      applicationId,
    })
  )?.application;
  if (!observed) throw step.state === "confirmed" || existingApplicationId ? conflict() : unknown();
  const oidc = observed.oidcConfiguration as Record<string, unknown> | undefined;
  const matches = (actual: unknown, expected: readonly string[]) =>
    Array.isArray(actual) &&
    actual.length === expected.length &&
    expected.every((item) => actual.includes(item));
  if (
    observed.applicationId !== applicationId ||
    observed.projectId !== projectId ||
    observed.name !== name ||
    observed.state !== "APPLICATION_STATE_ACTIVE" ||
    !oidc ||
    !validClientId(oidc.clientId) ||
    (createdClientId !== undefined && oidc.clientId !== createdClientId) ||
    (oidc.applicationType ?? web) !== web ||
    (oidc.authMethodType ?? basic) !== basic ||
    oidc.accessTokenType !== jwt ||
    oidc.nonCompliant === true ||
    oidc.developmentMode === true ||
    !matches(oidc.redirectUris, redirects) ||
    !matches(oidc.responseTypes, responseTypes) ||
    !matches(oidc.grantTypes, grantTypes)
  )
    throw conflict();
  try {
    const id = oidc.clientId;
    if (
      (await bounded(
        (operation) => options.hasSecret(applicationId, id, operation),
        AbortSignal.any([signal, AbortSignal.timeout(5000)]),
      )) !== true
    )
      throw unknown();
  } catch {
    throw unknown();
  }
  if (step.state !== "confirmed")
    await journal.confirm(ordinal, step.dispatchId, applicationId, signal);
  return { applicationId, clientId: oidc.clientId };
}
