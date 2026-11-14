import { HeimdallError } from "@techsavvyash/heimdall-sdk";
import { createDirectoryCleanup } from "@techsavvyash/heimdall-sdk/postgres";

export function directoryMapping() {
  const workspaces = JSON.parse(process.env.HEIMDALL_DIRECTORY_WORKSPACES ?? "null");
  if (
    !Array.isArray(workspaces) ||
    workspaces.length === 0 ||
    workspaces.length > 100 ||
    workspaces.some((id) => typeof id !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(id)) ||
    new Set(workspaces).size !== workspaces.length
  ) {
    throw new HeimdallError("invalid_argument");
  }
  return workspaces;
}

export function directoryCleanupRoute(pool, config, manifest, auth, sessions, origin) {
  const operator = process.env.HEIMDALL_DIRECTORY_OPERATOR_SUBJECT;
  if (!operator) return null;
  const workspaces = directoryMapping();
  const cleanup = createDirectoryCleanup(pool, {
    ...config,
    organizationId: process.env.HEIMDALL_DIRECTORY_ORGANIZATION,
  });
  return async (request, input) => {
    if (request.headers.get("origin") !== origin) throw new HeimdallError("forbidden");
    const actor = await sessions.authenticate(request);
    if (actor.subject !== operator) throw new HeimdallError("forbidden");
    const { subject, revision } = await input();
    const call = { signal: request.signal };
    // Authorize before the SDK binds the durable plan to the actor.
    for (const workspace of workspaces) {
      const roles = await auth.tenant(workspace).roles(actor, call);
      if (!roles.includes(manifest.ownerRole)) throw new HeimdallError("forbidden");
    }
    return cleanup.reconcile(auth, actor, { subject, revision, workspaces }, request.signal);
  };
}
