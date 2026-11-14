import { compileTenantModel } from "./tenant-model.js";
import { validateEndpoint, validClientId } from "./endpoints.js";
import { HeimdallError } from "./errors.js";

type Obj = Record<string, unknown>;
const invalid = (): never => {
  throw new HeimdallError("invalid_argument");
};
function object(value: unknown): Obj {
  if (!value || typeof value !== "object" || Array.isArray(value)) return invalid();
  return value as Obj;
}
function fields(value: unknown, required: string[], optional: string[] = []): Obj {
  const v = object(value);
  if (
    required.some((k) => !Object.hasOwn(v, k)) ||
    Object.keys(v).some((k) => !required.includes(k) && !optional.includes(k))
  )
    invalid();
  return v;
}
function string(value: unknown, max: number, pattern?: RegExp): string {
  if (
    typeof value !== "string" ||
    !value.isWellFormed() ||
    [...value].length < 1 ||
    [...value].length > max ||
    (pattern && !pattern.test(value))
  )
    return invalid();
  return value;
}
const slug = /^[a-z][a-z0-9_-]*(?![\s\S])/;
const role = /^[a-z][a-z0-9_]*(?![\s\S])/;
const permission = /^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*(?![\s\S])/;
const token = /^[A-Za-z0-9_-]+(?![\s\S])/;
function version(v: unknown) {
  if (typeof v !== "number" || !Number.isInteger(v) || v < 1 || v > 2147483647) invalid();
}
function profile(v: Obj): "production" | "loopback" {
  if (!Object.hasOwn(v, "transportProfile")) return "production";
  if (v.transportProfile !== "production" && v.transportProfile !== "loopback") return invalid();
  return v.transportProfile;
}
function list(value: unknown, min: number, max: number, check: (value: unknown) => void) {
  if (
    !Array.isArray(value) ||
    value.length < min ||
    value.length > max ||
    new Set(value).size !== value.length
  )
    invalid();
  for (const item of value as unknown[]) check(item);
}
function entries(value: unknown, min: number, max: number, pattern: RegExp) {
  const v = object(value);
  const entries = Object.entries(v);
  if (entries.length < min || entries.length > max) invalid();
  for (const [name] of entries) string(name, 64, pattern);
  return entries;
}
type DeploymentConfig = {
  schemaVersion: 1;
  productId: string;
  environment: string;
  issuer: string;
  audience: string;
  clientId: string;
  zitadelProjectId: string;
  clientKey: string;
  manifestVersion: number;
  openfga: { url: string; storeId: string; modelId: string };
  transportProfile?: "production" | "loopback";
};
export function validateDeploymentShape(value: unknown): void {
  const c = fields(
    value,
    [
      "schemaVersion",
      "productId",
      "environment",
      "issuer",
      "audience",
      "clientId",
      "zitadelProjectId",
      "clientKey",
      "manifestVersion",
      "openfga",
    ],
    ["transportProfile"],
  );
  if (c.schemaVersion !== 1) invalid();
  for (const key of ["productId", "environment", "clientKey"]) string(c[key], 64, slug);
  if (!validClientId(c.clientId)) invalid();
  string(c.zitadelProjectId, 128, token);
  string(c.audience, 2048);
  version(c.manifestVersion);
  const p = profile(c);
  validateEndpoint(string(c.issuer, 2048), p);
  const f = fields(c.openfga, ["url", "storeId", "modelId"]);
  validateEndpoint(string(f.url, 2048), p);
  string(f.storeId, 128, token);
  string(f.modelId, 128, token);
}
export function validateManifestShape(value: unknown): void {
  const m = fields(value, [
    "schemaVersion",
    "productId",
    "version",
    "clients",
    "permissions",
    "roles",
    "ownerRole",
    "delegation",
  ]);
  if (m.schemaVersion !== 1) invalid();
  string(m.productId, 64, slug);
  version(m.version);
  string(m.ownerRole, 64, role);
  for (const [, value] of entries(m.clients, 1, 32, slug)) {
    const client = fields(value, ["redirectUris"], ["transportProfile"]);
    const p = profile(client);
    list(client.redirectUris, 1, 32, (uri) => {
      const s = string(uri, 2048);
      validateEndpoint(s, p);
      if (!/^https?:\/\/[^/]+\//.test(s)) invalid();
    });
  }
  const checkPermission = (v: unknown) => {
    string(v, 128, permission);
  };
  list(m.permissions, 1, 256, checkPermission);
  for (const [, value] of entries(m.roles, 1, 128, role)) list(value, 0, 256, checkPermission);
  for (const [, value] of entries(m.delegation, 0, 128, role))
    list(value, 0, 128, (v) => {
      string(v, 64, role);
    });
}
function freeze<T>(value: T): T {
  if (value && typeof value === "object") {
    for (const child of Object.values(value)) freeze(child);
    Object.freeze(value);
  }
  return value;
}
// No file reads or network access: installed SDKs do not depend on checkout paths.
export function parseDeployment(configJSON: string, manifestJSON: string) {
  try {
    if (
      typeof configJSON !== "string" ||
      typeof manifestJSON !== "string" ||
      !configJSON.isWellFormed() ||
      !manifestJSON.isWellFormed() ||
      Buffer.byteLength(configJSON) > 1048576 ||
      Buffer.byteLength(manifestJSON) > 1048576
    )
      invalid();
    const config: unknown = JSON.parse(configJSON);
    const manifest: unknown = JSON.parse(manifestJSON);
    validateDeploymentShape(config);
    validateManifestShape(manifest);
    const c = config as DeploymentConfig;
    const m = manifest as Parameters<typeof compileTenantModel>[0];
    const artifact = compileTenantModel(m, c);
    return freeze({ config: c, manifest: m, artifact });
  } catch {
    throw new HeimdallError("invalid_argument");
  }
}
