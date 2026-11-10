---
title: Shared TypeScript and Go SDK contract
date: 2026-11-07
---

The November 10 decisions are locked in [[release-contract]], [[audit-contract]] and [[enterprise-contract]]. These extend the core interfaces below with mandatory durable audit and bounded identity revalidation. Earlier API sketches remain illustrative until encoded as schemas; enterprise delivery is mandatory in both SDKs.


The behavioral contract is selected; exact API spellings and concrete encodings remain implementation deliverables. TypeScript and Go implement the same behavior with idiomatic APIs. Examples below describe interfaces to build. Live-service tests must validate the implementation before release. This page refines the illustrative APIs in [[two-product-walkthrough]] and [[sdk-contract]].

## Adapter extension and precedence

[[design/built-in-adapters]] adds selected PostgreSQL sessions and optional managed workspaces. The interfaces below remain the core/product-owned path. Managed mode owns its operation journal and workspace/index transactions, requires a lifecycle gate on every permission/role check, and supplies Fetch/net/http handlers. Explicit migrations and bounded reconcile are separate operations.

The earlier onboarding example below illustrates desired-state reconciliation; it does not prove an uncertain attempt has stopped. In managed mode, an uncertain dispatched write remains pending/recovery_required and blocks conflicting changes and new access until verified quiescence. A reread or lease expiry alone cannot permit activation, inverse mutation or cleanup. This narrower safety rule supersedes any suggestion that every lost response automatically completes on retry.

## Common rules

The client is bound to product, environment, trusted issuer, audience, OpenFGA store, model and manifest version. Callers cannot override these per request. A verified principal contains issuer and subject, principal kind, and available authentication evidence. Session helpers return this verified principal. Trusted internal workflows can resolve another principal through a separate privileged identity API.

The proposed human-user object encoding is `user:` followed by unpadded base64url of UTF-8 compact JSON `[issuer,subject]`. Preserve the configured issuer exactly after discovery validation. Both languages must produce identical bytes, including JSON string escaping. Golden fixtures cover Unicode, punctuation and issuer differences. Keep tenant IDs immutable and validate length and permitted characters. Service principals require their own future contract.

| Behavior | TypeScript | Go |
| --- | --- | --- |
| Cancellation and deadline | Required request options carrying `AbortSignal` | `context.Context` as first parameter |
| Successful result | Resolved promise | Value and nil error |
| Failure | `HeimdallError` with stable `code` | Typed error supporting `errors.Is` against sentinel errors |
| Denied permission | `can` returns false, `require` throws forbidden | `Can` returns false with nil error, `Require` returns forbidden |
| Dependency failure | Throw unavailable or deadline error | Return corresponding error |
| Mutation after uncertain response | Return outcome-unknown error and operation ID where available | Same error category and operation ID |

Shared error codes include `unauthenticated`, `forbidden`, `invalid_argument`, `not_found`, `conflict`, `unavailable`, `deadline_exceeded`, `cancelled`, `audit_unavailable`, and `outcome_unknown`. Cancellation after a remote write may leave its outcome unknown. Do not automatically retry non-idempotent mutations. Credentials and token material must never appear in errors.

```ts
const actor = await auth.sessions.authenticate(request, { signal })
await auth.tenant(tenant.id).require(actor, 'invoice.approve', { signal })
```

```go
actor, err := auth.Sessions().Authenticate(ctx, r)
if err != nil { return err }
if err := auth.Tenant(tenant.ID).Require(ctx, actor, "invoice.approve"); err != nil {
    return err
}
```

The product still loads an active tenant and scopes the business query by tenant ID. Token-only APIs use `tokens.verify(accessToken, options)` or `Tokens().Verify(ctx, accessToken)` and enforce the same audience and identity rules.

## Sessions

A session manager runs inside the product backend. The browser receives an opaque random cookie. The product supplies durable storage, a stable encryption-key set, cookie policy and an allowlist of return destinations. The SDK owns OIDC state, nonce, PKCE, token validation, session rotation, and encrypted session payloads.

### Public API

```ts
const sessions = auth.createSessions({
  store: postgresSessionStore,
  encryptionKeys: sessionKeys,
  callbackUrl: 'https://ledger.example.com/auth/callback',
  allowedReturnTo: ['/workspaces', '/invoices'],
  cookie: { name: '__Host-ledger', secure: true, sameSite: 'lax' },
  absoluteLifetimeSeconds: 28_800,
  idleLifetimeSeconds: 1_800,
})

// Fetch Request/Response adapter. The framework mounts these handlers.
const loginResponse = await sessions.start(request, {
  returnTo: '/workspaces',
  signal,
})
const callbackResponse = await sessions.callback(request, { signal })
const actor = await sessions.authenticate(request, { signal })
const logoutResponse = await sessions.logout(request, { signal })
```

```go
sessions, err := auth.NewSessions(heimdall.SessionOptions{
    Store: postgresSessionStore,
    EncryptionKeys: sessionKeys,
    CallbackURL: "https://ledger.example.com/auth/callback",
    AllowedReturnTo: []string{"/workspaces", "/invoices"},
    Cookie: heimdall.CookieOptions{
        Name: "__Host-ledger", Secure: true, SameSite: http.SameSiteLaxMode,
    },
    AbsoluteLifetime: 8 * time.Hour,
    IdleLifetime: 30 * time.Minute,
})
if err != nil { return err }

// net/http adapter. Each handler is mounted by the product.
err = sessions.Start(ctx, w, r, heimdall.LoginOptions{ReturnTo: "/workspaces"})
err = sessions.Callback(ctx, w, r)
actor, err := sessions.Authenticate(ctx, r)
err = sessions.Logout(ctx, w, r)
```

The route snippets show separate handler operations. TypeScript's adapter returns a response containing redirects or cookie changes. Go writes the equivalent response and returns an error for adapter handling. Cookie defaults require Secure, HttpOnly, Path=/ and no Domain for the proposed `__Host-` names. A return URL must match the configured allowlist. Logout requires a CSRF-protected POST. The product applies CSRF protection to its other cookie-authenticated state-changing routes.

For tenant-specific login, an optional `tenantId` is resolved through a product-supplied tenant-login resolver. The resolver returns a trusted organization mapping and allowed login policy. The SDK binds that policy to login state and validates the resulting authentication evidence. A browser-supplied organization ID cannot directly select privileged configuration. Strict SSO policy validation remains a live-service acceptance requirement.

### Minimal storage interface

```ts
type StoredRecord = {
  value: Uint8Array
  version: string
  expiresAt: Date
}

interface SessionStore {
  create(key: string, value: Uint8Array, expiresAt: Date,
    options: RequestOptions): Promise<boolean>
  read(key: string, options: RequestOptions): Promise<StoredRecord | null>
  compareAndSwap(key: string, expectedVersion: string,
    value: Uint8Array, expiresAt: Date,
    options: RequestOptions): Promise<boolean>
  delete(key: string, options: RequestOptions): Promise<void>
}
```

```go
type StoredRecord struct {
    Value []byte
    Version string
    ExpiresAt time.Time
}

type SessionStore interface {
    Create(context.Context, string, []byte, time.Time) (bool, error)
    Read(context.Context, string) (*StoredRecord, error)
    CompareAndSwap(context.Context, string, string, []byte, time.Time) (bool, error)
    Delete(context.Context, string) error
}
```

`create` succeeds only for an absent key. `read` returns absent for expired records. `compareAndSwap` succeeds only against the current unexpired version and never recreates deleted records. Versions change on every write and cannot repeat for a reused key. `delete` is idempotent. All operations must be atomic across product replicas. The SDK encrypts opaque values and namespaces keys by product, environment and record type. The store owns TTL enforcement and eventual physical cleanup.

Login callback atomically claims its login record before exchanging the code. A second callback is rejected. The login record is also bound to a browser cookie so stolen state alone cannot complete another browser's login.

### Refresh and logout races

```mermaid
sequenceDiagram
  participant R1 as Request on replica A
  participant Store as Session store
  participant R2 as Request on replica B
  participant Z as ZITADEL
  R1->>Store: Compare-and-swap active session to refreshing
  Store-->>R1: Claimed refresh attempt
  R2->>Store: Read session
  Store-->>R2: Refreshing state
  R1->>Z: Refresh token once
  alt Result and persistence confirmed
    Z-->>R1: New tokens
    R1->>Store: Compare-and-swap refreshing version to active
    R2->>Store: Bounded retry reads updated session
  else Response lost or process crashes
    R2->>Store: Expired refresh attempt becomes reauth required
  end
```

Only one refresh attempt may own a session version. Other requests wait within their deadline or receive an unavailable response. If refresh outcome is uncertain, require reauthentication rather than replay a potentially consumed refresh token. This intentionally trades a new login after rare failures for deterministic behavior. A crashed attempt has a bounded lifetime, after which the session becomes unusable.

Local logout atomically deletes the stored session and clears the cookie. Refresh persistence uses compare-and-swap, so it cannot resurrect a deleted session. Upstream token revocation or provider-wide logout is an explicit additional policy. Already-authenticated in-flight product operations follow the revocation limits in [[operations]].

## Workspace discovery

The application needs workspace IDs, names and lifecycle status. The proposed baseline uses product-owned keyset pagination over active tenant candidates, with SDK membership checks for each batch. This avoids binding the public pagination contract to unverified OpenFGA list behavior. It can be expensive when a user belongs to very few tenants in a large product. Optimize the candidate source with a product membership index when measured scale requires it, while retaining fresh authorization checks.

### Public API and product driver

```ts
const workspaces = auth.createWorkspaceDiscovery({ source: workspaceSource })
const page = await workspaces.list(actor, {
  cursor: requestCursor,
  limit: 20,
  signal,
})
// { items: Workspace[], nextCursor: string | null }

interface WorkspaceSource {
  scan(input: {
    after: string | null
    limit: number
  }, options: RequestOptions): Promise<{
    items: Array<{ workspace: Workspace; position: string }>
    exhausted: boolean
  }>
}
type Workspace = { id: string; name: string }
```

```go
discovery := auth.NewWorkspaceDiscovery(workspaceSource)
page, err := discovery.List(ctx, actor, heimdall.WorkspaceListOptions{
    Cursor: requestCursor,
    Limit: 20,
})
// page.Items and page.NextCursor

type WorkspaceSource interface {
    Scan(context.Context, ScanOptions) (CandidatePage, error)
}
type ScanOptions struct {
    After string
    Limit int
}
type Candidate struct {
    Workspace Workspace
    Position string
}
type CandidatePage struct {
    Items []Candidate
    Exhausted bool
}
```

The source queries the product's active tenants in a stable, unique order, such as creation ID plus tenant ID. Positions resume strictly after the corresponding candidate. It returns at most the requested limit and must advance or report exhaustion. Names and tenant identifiers are validated before returning a public result.

```mermaid
flowchart TD
  Request[Principal and signed cursor] --> Validate[Validate cursor binding and expiry]
  Validate --> Scan[Product scans active tenant candidates]
  Scan --> Check[SDK checks member relation for each candidate]
  Check --> Fill{Page full or work budget reached}
  Fill -->|No and more candidates| Scan
  Fill -->|Yes| Return[Allowed workspaces and next cursor]
  Fill -->|Source exhausted| End[Allowed workspaces and terminal cursor]
```

### Pagination semantics

- Default page size 20, maximum 100. Proposed default scan budget is 500 candidates per request, configurable by the backend.
- Preserve candidate order. Return only active candidates with a completed allow decision for membership.
- Return a short or empty page with a continuation cursor when the scan budget is reached. The UI continues while a cursor exists. Totals are omitted.
- The next cursor records the last consumed candidate. If a batch contains more allowed results than fit, resume after the last consumed item so later items remain eligible.
- SDK cursors are authenticated and bind product, environment, principal, source position, model ID, format version and expiry. All replicas share configured cursor keys. Cursors are opaque to clients.
- Dependency failure fails the page request. It must not silently filter failed checks as denials or advance a returned cursor past them.
- This is a live traversal. Changes behind the cursor can appear on a fresh traversal. No snapshot guarantee is made. Every subsequent protected operation checks current access again.

An optimized OpenFGA-based or indexed candidate source must preserve these semantics before becoming a supported alternative. Both SDKs run the same fixture with sparse membership, deleted tenants, identical display names and concurrent changes.

## Tenant onboarding and retries

The product owns authorization to create workspaces, idempotency records, tenant transactions and retry scheduling. The SDK exposes an idempotent initial-owner operation. A successful operation means the expected relationships were confirmed. Activation is a separate product database commit.

### Public API

```ts
const intent = await tenantRepo.beginOnboarding({
  productId: config.productId,
  operationId: idempotencyKey,
  owner: actor.identity,
  name: 'Acme',
})

// Runs under the product's durable per-tenant mutation serialization.
if (intent.status !== 'active') {
  const receipt = await auth.provisioning.ensureTenantOwner({
    operationId: intent.operationId,
    tenantId: intent.tenantId,
    owner: intent.owner,
  }, { signal })
  await tenantRepo.activateIfPending(intent, receipt)
}
```

```go
intent, err := tenantRepo.BeginOnboarding(ctx, BeginOnboardingInput{
    ProductID: config.ProductID,
    OperationID: idempotencyKey,
    Owner: actor.Identity(),
    Name: "Acme",
})
if err != nil { return err }

// Product serializes all lifecycle mutations for this tenant.
if intent.Status != Active {
    receipt, err := auth.Provisioning().EnsureTenantOwner(ctx, heimdall.TenantOwnerInput{
        OperationID: intent.OperationID,
        TenantID: intent.TenantID,
        Owner: intent.Owner,
    })
    if err != nil { return err }
    if err := tenantRepo.ActivateIfPending(ctx, intent, receipt); err != nil {
        return err
    }
}
```

These are product workflow bodies, not standalone endpoints. `tenantRepo` is application code. Both examples assume authenticated creation authorization, payload validation, and a serialized workflow around bootstrap, cancellation and activation. The SDK's operation ID is for correlation. Durable idempotency is provided by the product's stored intent.

### Required product operation record

| Field | Contract |
| --- | --- |
| Product, environment and operation ID | Unique idempotency scope |
| Initiating principal and payload fingerprint | Bind retries to the same caller and request |
| Tenant ID | Allocated once and never reused |
| Initial owner issuer and subject | Immutable after operation creation |
| State | Pending, active, cancelled or cleanup required |
| Attempt and outcome | Diagnostic correlation and retry status |

Same key and same payload returns the existing operation. Same key with another owner, name or payload returns conflict. Unrelated callers cannot recover another caller's operation through its idempotency key. Setup retries for product resources use a separate operator provisioning record.

### SDK ensure behavior

1. Validate the pending intent input and principal encoding within a privileged workflow.
2. Read the required member and owner relationships using the strongest supported consistency for reconciliation.
3. If an unexpected existing owner is observed, return conflict. The product's serialization prevents competing bootstrap operations. The SDK read cannot provide an atomic global uniqueness guarantee.
4. Write missing relationships. Handle concurrent duplicate writes by rereading and verifying desired state.
5. Confirm membership and ownership and return a receipt containing operation ID, tenant ID, principal and model ID.
6. On uncertain write outcome, return `outcome_unknown`. The caller retries the same recorded intent. Successful grants are preserved for reconciliation.

```mermaid
sequenceDiagram
  participant Product as Product workflow
  participant DB as Product database
  participant SDK as Provisioning SDK
  participant F as OpenFGA
  Product->>DB: Create or load unique pending intent
  DB-->>Product: Stable tenant, owner and operation
  Product->>SDK: Ensure recorded owner
  SDK->>F: Read and write missing relationships
  F-->>SDK: Grant committed but response lost
  SDK-->>Product: Outcome unknown with operation ID
  Product->>DB: Keep pending and record retry
  Product->>SDK: Retry same intent
  SDK->>F: Reconcile and confirm desired relationships
  F-->>SDK: Confirmed
  SDK-->>Product: Receipt
  Product->>DB: Conditional activation and completion
```

Cancellation and grant writes must use the same product serialization mechanism. A stale worker must not recreate grants after cleanup. Lease expiry alone is insufficient fencing for an external service write. Use a serialized workflow with recovery that reconciles any in-flight attempt before final cleanup. Keep that boundary explicit in the integration recipe.

## Shared conformance cases

| Area | Required fixture or integration test |
| --- | --- |
| Identity | Both languages encode identical issuer and subject pairs |
| Sessions | Callback replay rejected across replicas |
| Sessions | Concurrent refresh uses one owner, lost refresh result requires login |
| Sessions | Logout during refresh cannot resurrect session |
| Discovery | Sparse memberships return continuation even for empty pages |
| Discovery | Page-full mid-batch resumes without skipping allowed candidates |
| Discovery | Wrong-principal cursor rejected, dependency errors fail the request |
| Onboarding | Same key and payload produces one tenant and owner |
| Onboarding | Changed owner or payload conflicts |
| Onboarding | Lost response quarantines uncertain dispatch; confirmed grant plus failed activation permits safe local recovery |
| Onboarding | Cancelled operation cannot be revived by a stale worker |

The principal encoding, storage versions, cursor format and error codes belong in `contracts/` with fixtures. The implementations belong in both SDKs. The product persistence recipes belong in Ledger and Support examples. See [[project-structure]] and [[task-list]].
