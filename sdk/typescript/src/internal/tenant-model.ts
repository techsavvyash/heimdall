import { createHash } from "node:crypto";
import { HeimdallError } from "./errors.js";
import { validateManifestBinding } from "./manifest.js";

type Manifest = Parameters<typeof validateManifestBinding>[0];
type Binding = Parameters<typeof validateManifestBinding>[1];
type Json = string | number | boolean | null | Json[] | { [key: string]: Json };
const ref = (relation: string): Json => ({ computedUserset: { relation } });
const union = (grants: Json[]): Json =>
  grants.length === 0
    ? { difference: { base: ref("member"), subtract: ref("member") } }
    : grants.length === 1
      ? grants[0]!
      : { union: { child: grants } };
const intersect = (left: Json, right: Json): Json => ({ intersection: { child: [left, right] } });
function canonical(value: Json): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value !== null && typeof value === "object")
    return `{${Object.keys(value)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${canonical(value[key]!)}`)
      .join(",")}}`;
  return JSON.stringify(value);
}

// Internal compiler: structural manifest validation must precede this call.
// Relation ordinals are bound to this exact model/manifest, never inferred at request time.
export function compileTenantModel(manifest: Manifest, binding: Binding) {
  validateManifestBinding(manifest, binding);
  const roles: Record<string, { assignment: string; effective: string; delegate: string }> = {};
  const permissions: Record<string, string> = {};
  const relations: Record<string, Json> = { member: { this: {} } };
  const metadata: Record<string, Json> = {
    member: { directly_related_user_types: [{ type: "user" }] },
  };
  Object.keys(manifest.roles)
    .sort()
    .forEach((role, index) => {
      const assignment = `r_${index}`;
      const effective = `e_${index}`;
      roles[role] = { assignment, effective, delegate: `d_${index}` };
      relations[assignment] = { this: {} };
      metadata[assignment] = { directly_related_user_types: [{ type: "user" }] };
      relations[effective] = intersect(ref("member"), ref(assignment));
    });
  [...manifest.permissions].sort().forEach((permission, index) => {
    const relation = `p_${index}`;
    permissions[permission] = relation;
    const grants = Object.keys(roles)
      .filter((role) => manifest.roles[role]!.includes(permission))
      .map((role) => ref(roles[role]!.assignment));
    relations[relation] = intersect(ref("member"), union(grants));
  });
  for (const target of Object.keys(roles)) {
    const grants =
      target === manifest.ownerRole
        ? []
        : Object.keys(roles)
            .filter(
              (actor) =>
                Object.hasOwn(manifest.delegation, actor) &&
                manifest.delegation[actor]!.includes(target),
            )
            .map((actor) => ref(roles[actor]!.assignment));
    relations[roles[target]!.delegate] = intersect(ref("member"), union(grants));
  }

  const model: Json = {
    schema_version: "1.1",
    type_definitions: [
      { type: "user" },
      { type: "workspace", relations, metadata: { relations: metadata } },
    ],
  };
  const modelHash = createHash("sha256").update(canonical(model)).digest("hex");
  return { model, modelHash, roles, permissions };
}

// This resolves a declared name, not actor authorization. The delegate relation
// must be checked with fresh membership before any audited mutation is dispatched.
export function roleRelations(model: ReturnType<typeof compileTenantModel>, role: string) {
  if (!Object.hasOwn(model.roles, role)) throw new HeimdallError("invalid_argument");
  return model.roles[role]!;
}
