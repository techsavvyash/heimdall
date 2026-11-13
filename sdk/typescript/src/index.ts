export { createHeimdall } from "./runtime.js";
export type {
  Heimdall,
  Tenant,
  RuntimeOptions,
  SessionOptions,
  Sessions,
  RuntimeStorage,
  IdentityGate,
  AuditAppend,
  AuditReceipt,
  Principal,
} from "./runtime.js";
export { HeimdallError } from "./internal/errors.js";
export { auditDigest as auditEventDigest } from "./internal/audit-receipts.js";

export type {
  DiscoveryOptions,
  Workspace,
  WorkspaceSource,
  WorkspaceDiscovery,
} from "./internal/workspace-discovery.js";
