import { HeimdallError } from "../internal/errors.js";
import { providerId } from "./provider-request.js";

export type DirectoryGrantSource =
  | { kind: "independent"; roles: readonly string[] }
  | { kind: "directory"; ruleId: string; roles: readonly string[] };

/** Plan a rule removal from current direct roles and complete product-owned grant provenance. */
export function planDirectoryRevocation(input: {
  ruleId: string;
  ownerRole: string;
  currentRoles: readonly string[];
  sources: readonly DirectoryGrantSource[];
}) {
  const { ruleId, ownerRole, currentRoles, sources } = input;
  const role = (value: unknown): value is string =>
    typeof value === "string" && /^[a-z][a-z0-9_]{0,63}(?![\s\S])/.test(value);
  const roleList = (values: unknown): values is readonly string[] =>
    Array.isArray(values) &&
    values.length <= 128 &&
    values.every(role) &&
    new Set(values).size === values.length;
  if (
    !providerId(ruleId) ||
    !role(ownerRole) ||
    !roleList(currentRoles) ||
    !Array.isArray(sources) ||
    sources.length > 128
  )
    throw new HeimdallError("invalid_argument");

  const retained = new Set<string>();
  const rules = new Set<string>();
  let removed: readonly string[] | undefined;
  let keepsMembership = false;
  for (const source of sources) {
    if (!source || !roleList(source.roles)) throw new HeimdallError("invalid_argument");
    if (source.kind === "directory") {
      if (
        !providerId(source.ruleId) ||
        rules.has(source.ruleId) ||
        source.roles.includes(ownerRole)
      )
        throw new HeimdallError("invalid_argument");
      rules.add(source.ruleId);
      if (source.ruleId === ruleId) {
        removed = source.roles;
        continue;
      }
    } else if (source.kind !== "independent") {
      throw new HeimdallError("invalid_argument");
    }
    keepsMembership = true;
    for (const name of source.roles) retained.add(name);
  }
  if (!removed) throw new HeimdallError("not_found");
  if (currentRoles.includes(ownerRole)) throw new HeimdallError("forbidden");
  const roles = currentRoles.filter((name) => !removed.includes(name) || retained.has(name)).sort();
  if (roles.length === currentRoles.length && (keepsMembership || roles.length > 0))
    return { action: "unchanged" as const, roles };
  return {
    action:
      keepsMembership || roles.length > 0 ? ("assign_roles" as const) : ("remove_member" as const),
    roles,
  };
}
