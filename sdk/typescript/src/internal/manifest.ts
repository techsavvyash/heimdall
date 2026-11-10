import { HeimdallError } from "./errors.js";

type Manifest = {
  schemaVersion: 1;
  productId: string;
  version: number;
  clients: Record<string, { redirectUris: string[] }>;
  permissions: string[];
  roles: Record<string, string[]>;
  ownerRole: string;
  delegation: Record<string, string[]>;
};
type Binding = { productId: string; manifestVersion: number; clientKey: string };

// Structural schema validation must precede this cross-reference validation.
export function validateManifestBinding(manifest: Manifest, config: Binding): void {
  const invalid = () => {
    throw new HeimdallError("invalid_argument");
  };
  if (
    config.productId !== manifest.productId ||
    config.manifestVersion !== manifest.version ||
    !Object.hasOwn(manifest.clients, config.clientKey) ||
    !Object.hasOwn(manifest.roles, manifest.ownerRole)
  )
    invalid();
  const permissions = new Set(manifest.permissions);
  for (const grants of Object.values(manifest.roles)) {
    for (const permission of grants) {
      if (!permissions.has(permission)) invalid();
    }
  }
  for (const [actorRole, targets] of Object.entries(manifest.delegation)) {
    if (!Object.hasOwn(manifest.roles, actorRole)) invalid();
    for (const target of targets) {
      if (!Object.hasOwn(manifest.roles, target) || target === manifest.ownerRole) invalid();
    }
  }
}
