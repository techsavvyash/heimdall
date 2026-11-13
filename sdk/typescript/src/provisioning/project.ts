import type { Pool } from "pg";
import { HeimdallError } from "../internal/errors.js";
import { providerId, zitadel } from "./zitadel.js";
import { setupJournal } from "./journal.js";

type Options = {
  scope: Parameters<typeof setupJournal>[1];
  issuer: string;
  organizationId: string;
  name: string;
  token: string;
  transportProfile?: "production" | "loopback";
  existingProjectId?: string;
};

export async function provisionProject(pool: Pool, options: Options, signal: AbortSignal) {
  const { scope, issuer, organizationId, name, token, existingProjectId } = options;
  if (
    !providerId(organizationId) ||
    (existingProjectId !== undefined && !providerId(existingProjectId)) ||
    typeof name !== "string" ||
    !name.isWellFormed() ||
    !name.trim() ||
    [...name].length > 200
  )
    throw new HeimdallError("invalid_argument");
  const journal = setupJournal(pool, scope);

  const call = zitadel(issuer, token, options.transportProfile ?? "production", signal);
  const service = "/zitadel.project.v2.ProjectService/";
  const step = await journal.claim(1, signal);
  const projectId = existingProjectId ?? step.dispatchId;
  if (step.state === "confirmed" && step.resourceId !== projectId)
    throw new HeimdallError("conflict");
  if (step.state === "claimed" && !existingProjectId) {
    try {
      const created = await call<{ projectId: string }>(service + "CreateProject", {
        projectId,
        organizationId,
        name,
      });
      if (created?.projectId !== projectId) throw new Error();
    } catch {
      throw new HeimdallError("outcome_unknown", step.dispatchId);
    }
  }
  const observed = (
    await call<{ project: Record<string, unknown> }>(service + "GetProject", { projectId })
  )?.project;
  if (!observed) {
    if (step.state !== "confirmed" && !existingProjectId)
      throw new HeimdallError("outcome_unknown", step.dispatchId);
    throw new HeimdallError("conflict", step.dispatchId);
  }
  if (
    observed.projectId !== projectId ||
    observed.organizationId !== organizationId ||
    observed.name !== name ||
    observed.state !== "PROJECT_STATE_ACTIVE" ||
    [
      observed.projectRoleAssertion,
      observed.authorizationRequired,
      observed.projectAccessRequired,
    ].some((value) => value !== undefined && value !== false)
  )
    throw new HeimdallError("conflict", step.dispatchId);
  if (step.state !== "confirmed") await journal.confirm(1, step.dispatchId, projectId, signal);
  return projectId;
}
