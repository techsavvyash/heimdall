import type { Pool } from "pg";
import { validateEndpoint } from "../internal/endpoints.js";
import { checkCancellation, HeimdallError } from "../internal/errors.js";
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
  validateEndpoint(issuer, options.transportProfile ?? "production");
  const validId = (value: string) =>
    typeof value === "string" && /^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(value);
  if (
    new URL(issuer).pathname !== "/" ||
    !validId(organizationId) ||
    (existingProjectId !== undefined && !validId(existingProjectId)) ||
    typeof name !== "string" ||
    !name.isWellFormed() ||
    !name.trim() ||
    [...name].length > 200 ||
    typeof token !== "string" ||
    !/^[\x21-\x7e]{1,4096}(?![\s\S])/.test(token)
  )
    throw new HeimdallError("invalid_argument");
  const journal = setupJournal(pool, scope);

  async function call(method: string, body: Record<string, string>) {
    const operation = AbortSignal.any([signal, AbortSignal.timeout(5000)]);
    checkCancellation(operation);
    try {
      const response = await fetch(
        issuer.replace(/\/$/, "") + "/zitadel.project.v2.ProjectService/" + method,
        {
          method: "POST",
          redirect: "error",
          signal: operation,
          headers: {
            "content-type": "application/json",
            "connect-protocol-version": "1",
            authorization: "Bearer " + token,
          },
          body: JSON.stringify(body),
        },
      );
      if (!response.ok) {
        await response.body?.cancel();
        if (response.status === 404) return null;
        throw new Error();
      }
      if (!response.body) throw new Error();
      const reader = response.body.getReader();
      const chunks: Uint8Array[] = [];
      let bytes = 0;
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          bytes += value.length;
          if (bytes > 1048576) throw new Error();
          chunks.push(value);
        }
      } finally {
        await reader.cancel();
      }
      return JSON.parse(Buffer.concat(chunks, bytes).toString("utf8")) as {
        projectId?: string;
        project?: Record<string, unknown>;
      };
    } catch {
      checkCancellation(operation);
      throw new HeimdallError("unavailable");
    }
  }

  const step = await journal.claim(1, signal);
  const projectId = existingProjectId ?? step.dispatchId;
  if (step.state === "confirmed" && step.resourceId !== projectId)
    throw new HeimdallError("conflict");
  if (step.state === "claimed" && !existingProjectId) {
    try {
      const created = await call("CreateProject", { projectId, organizationId, name });
      if (created?.projectId !== projectId) throw new Error();
    } catch {
      throw new HeimdallError("outcome_unknown", step.dispatchId);
    }
  }
  const observed = (await call("GetProject", { projectId }))?.project;
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
