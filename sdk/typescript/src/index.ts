export { createHeimdall } from "./runtime.js";
export type {
  Heimdall,
  Tenant,
  RuntimeOptions,
  WorkspaceLifecyclePolicy,
  SessionOptions,
  Sessions,
  RuntimeStorage,
  IdentityGate,
  AuditAppend,
  AuditReceipt,
  Principal,
} from "./runtime.js";
export { HeimdallError } from "./internal/errors.js";
export type {
  SessionStore,
  SessionStoreOptions,
  StoredSessionRecord,
} from "./internal/custom-session-store.js";
export { auditDigest as auditEventDigest } from "./internal/audit-receipts.js";

export type {
  DiscoveryOptions,
  Workspace,
  WorkspaceSource,
  WorkspaceDiscovery,
} from "./internal/workspace-discovery.js";

export type {
  WorkspaceOnboarding,
  WorkspaceOnboardingStatus,
  WorkspaceCreateInput,
  WorkspaceCreationPolicy,
} from "./internal/managed-onboarding.js";

export type {
  MembershipInput,
  MembershipStatus,
  Memberships,
} from "./internal/managed-memberships.js";

export type { ReconcileOptions, ReconcileResult } from "./internal/reconcile-operations.js";
