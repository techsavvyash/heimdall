---
title: First-release enterprise SSO, SCIM and SIEM
date: 2026-11-10
---

Enterprise capabilities are mandatory in the first release of both SDKs. This page fixes ownership, default policy, failure behavior and acceptance cases. Upstream capabilities must be verified against pinned versions before a feature is described as implemented.

## Tenant SAML and OIDC

A product-owned mapping associates a workspace with a verified ZITADEL organization and a versioned login policy. Acme Ledger and Acme Support can share one organization when they deliberately share provider policy; separate organization contexts are needed for independently administered provider configuration. Membership remains independent.

Authorized enterprise settings workflows configure an organization-scoped SAML or OIDC provider through privileged SDK helpers. The product validates administrator authority and organization ownership. The customer's identity administrator supplies registration/metadata and configures its provider. Setup returns callback/service-provider details, then requires a test login before activation. Secrets use operator/product secret storage and never appear in generated public configuration or audit attributes.

[[evidence/phase-6-provider-setup]] records the SDK setup and test-login APIs, the administrator workflow and pinned-service acceptance.

```mermaid
sequenceDiagram
  participant User
  participant Product
  participant Z as ZITADEL
  participant IdP as Acme SAML or Globex OIDC
  User->>Product: Login for tenant
  Product->>Product: Resolve trusted organization and policy
  Product->>Z: OIDC login bound to tenant policy and state
  Z->>IdP: Authenticate with selected upstream provider
  IdP-->>Z: Authentication result
  Z-->>Product: Product OIDC callback
  Product->>Product: Validate identity, policy evidence and membership
  Product-->>User: Session or denial
```

Strict enterprise mode is SSO-only: no password fallback, email-based automatic account linking or implicit guest bypass. Existing sessions and every tenant switch must satisfy the target tenant's policy version and provider/organization authentication evidence; otherwise start compliant reauthentication or deny. Evidence must come from verified provider claims/APIs, not a browser organization hint. If the pinned ZITADEL release cannot prove required evidence, strict mode cannot ship by guessing. Break-glass operator access is an explicit audited deployment procedure, not a hidden local-login route. Guest/fallback policies may be explicitly configured and tested, but never inferred from membership.

[[evidence/phase-2-provider-organization]] records a pinned-service constraint: the user-link APIs reject an organization-scoped provider outside the user's home organization, even with the bootstrap administrator. A workspace membership does not bypass that boundary. The selected v1 model uses separate tenant-owned accounts for independently administered directories, verified in [[evidence/phase-2-two-tenant-federation]] and now accepted by the user. Stable-subject linking across those directories is outside v1. A callback with a different issuer/subject must use that principal's own memberships and must not carry over prior identity authority. Instance-level provider alternatives remain unapproved.

Provider links within a user’s owning organization require verified identity ownership and authorized configuration. Cross-directory account merging is outside v1; matching email alone cannot merge principals. Bind identities by issuer/subject. Provider-policy changes invalidate applicable policy evidence so existing sessions are re-evaluated. Users switching between Acme SAML and Globex OIDC cannot reuse noncompliant evidence just because they belong to both.

Pinned-service progress: [[evidence/phase-2-federation-binding]] verifies an intent-to-session-to-token evidence chain for both SAML and OIDC. The implementation still needs strict target-policy and browser-transaction checks, and atomic persistence. [[evidence/phase-2-enterprise-contract]] records the structured evidence schema and shared TS/Go policy-gate amendment; those internal validators do not verify provider provenance. This is evidence of capability, not completed SDK enforcement.

[[evidence/phase-6-tenant-sso-gate]] documents the TS and Go authorization gate for existing sessions and workspace access, along with deployed provider login and account switching. Strict workspaces require saved evidence matching the current trusted policy before authorization can evaluate membership or roles.

[[evidence/phase-2-self-service-evidence]] verifies session reads with an API-audience user token, avoiding a Login PAT for that read. The returned intent factor contains only a timestamp; it cannot replace verified provider ID/upstream subject provenance. Ordinary hosted-login callback integration remains unproven for the full strict-evidence chain.

## SCIM users and lifecycle

SCIM requests go directly to ZITADEL's organization-scoped service. The customer directory configures endpoint and least-privilege credentials. The SDK does not introduce a SCIM server. Current upstream documentation describes user lifecycle support and excludes SCIM Group provisioning; group-to-role synchronization is not a first-release capability claim. [ZITADEL SCIM guide](https://zitadel.com/docs/guides/manage/user/scim2)

Each product supplies explicit organization-to-workspace enrollment rules. Default is no automatic membership on directory creation. An authorized configuration can opt into specified workspaces and product-defined starting roles, but never implicit owner privileges. Preserve grant provenance so disabling one enrollment rule removes its grants without deleting unrelated independently administered grants. Identity deactivation blocks that identity across affected product sessions; product role cleanup respects the configured mapping.

```mermaid
flowchart LR
  Directory[Customer directory] -->|SCIM user lifecycle| Z[ZITADEL]
  Z -->|verified lifecycle state| Worker[Product worker with SDK helpers]
  Worker --> Intent[(Durable lifecycle intents and checkpoints)]
  Worker --> Deny[Local identity deny and session invalidation]
  Worker --> F[Mapped membership reconciliation]
  Worker --> Audit[Durable audit outbox]
```

Workers use verified events or bounded polling according to available service APIs, with full reconciliation for gaps. Re-read authoritative lifecycle state before applying a replayed change; out-of-order activation must not undo a later deactivation. Persist a durable per-subject operation before advancing ingestion checkpoints; checkpointing ingestion does not assert remote application completed. Per-product failures remain pending independently. Export lag and lifecycle lag are observable. Run workers explicitly through the product scheduler.

[[evidence/phase-6-scim-reader]] records the SDK read path and its live credential, provenance and state checks.

[[evidence/phase-6-scim-polling]] records the polling adapter's durable discovery checkpoint and known-subject reconciliation.

[[evidence/phase-6-directory-job]] shows the runnable Ledger and Support jobs and their scheduler wiring.

Once deactivation is observed, first persist a local identity deny/session invalidation in managed mode, then reconcile mapped memberships through the uncertainty-safe mutation path. Core consumers implement the equivalent identity gate interface. This prevents a delayed remote tuple removal from permitting new access in that product. The identity freshness contract in [[release-contract]] additionally covers requests before event ingestion catches up. Worker target under healthy dependencies is observation and deny propagation within 60 seconds; measure it in integration tests and expose lag. Outages do not have an invented bounded completion promise: stale authoritative identity status fails closed after its configured freshness, and pending remote changes retain their recovery gate.

[[evidence/phase-6-scim-deactivation]] records the PostgreSQL transaction that persists the cleanup intent and local denial together.

Reactivation does not resurrect all historical role tuples. Confirm cleanup and current approved enrollment rules, then grant only the selected roles under a new audited operation. Inactive/deleted principals cannot accept invitations. Directory deletion preserves required operation/audit tombstones under the configured privacy/retention policy; external IDs never replace issuer/subject as principal identity.

## SIEM and custom events

[[audit-contract]] is mandatory for all decisions, role changes and transfers. Product/user-configured event types are supported extensions, not an exemption to this coverage. The product operates durable storage and a scheduled exporter; the SDK supplies adapters and bounded delivery/reconciliation APIs. Existing customer SIEM/collector infrastructure receives events. No companion Heimdall service is introduced.

[[evidence/phase-6-audit-delivery]] describes the PostgreSQL exporter and the authenticated HTTP receipt protocol.

Correlate ZITADEL identity/configuration events with product audit using verified organization mappings, issuer/subject and trace identifiers where available. Do not invent a common request ID for unrelated upstream events. Identity collector and product exporter have separate checkpoints. Shared organizations may require authorized fan-out to several products, with explicit filtering of unrelated tenant data.

## Required dry runs

| Scenario | Expected result |
| --- | --- |
| Acme SAML and Globex OIDC on one deployment | Correct tenant provider, independent memberships and product audiences |
| Existing password session enters strict Acme | Compliant reauthentication or deny |
| Tenant administrator submits another organization's ID | Reject before provider changes |
| Same email from unrelated IdP | No automatic principal merge |
| SCIM creates employee | Identity exists; access only from explicit enrollment rules |
| SCIM deactivates member of two products; one worker fails | Identity gate/freshness prevents stale access; each product tracks pending reconciliation |
| Old activation event replayed after disable | Authoritative inactive state wins |
| Reactivation after stale role tuples | Cleanup and current grants only; no revived privileges |
| SIEM rejects one custom event | Preserve/quarantine and alert; other valid scopes can progress |
| SIEM unavailable | Durable backlog; local capacity policy blocks rather than drops when exhausted |

## Implementation and release

#44 owns provider setup and strict SSO; #45 verifies SCIM capability; #47 implements lifecycle reconciliation; #46 implements durable audit; #48 implements SIEM delivery. #50 depends on all of them plus adapter safety and compatibility. Live-service assertions, TS/Go parity and a documented operator setup are required. The docs may be published now, but no deployed auth functionality is implied.

[[evidence/phase-2-federation-credentials]] verifies a narrower instance-scoped session role for SDK-controlled intent/session/OIDC flows. The broad Login client role is unnecessary for that measured chain, but browser binding and strict integration remain open. A server-side SDK credential is still required; this does not introduce a companion service or tenant-isolated infrastructure authority.

Provider intent expiry must not substitute for the local browser-transaction deadline. The pinned ZITADEL implementation uses explicit upstream expiry ahead of its fallback lifetime; see [[evidence/phase-2-federation-binding]]. Require successful, unexpired session creation and signed binding before persisting evidence, while independently enforcing local transaction expiry and target policy age.

## Pinned event collection constraint

[[evidence/phase-2-lifecycle-events]] verifies aggregate-local event sequences and an instance-scoped reader. Never use one numeric sequence across users or organizations. Known-identity lifecycle checks can reconcile authoritative state with per-aggregate checkpoints; complete discovery/export still requires verified gap handling. Historical activation events cannot override a current inactive/deleted authoritative state.

Bounded time-window scans can discover multiple aggregates, as measured in the same evidence page, but short pages alone do not certify a complete moving time watermark. The pinned reader drains instance writers through an advisory lock and applies a transaction-time position cap; retention and unsupported time boundaries still require explicit handling. Export must retain its checkpoint on incomplete/saturated windows and require a verified visibility strategy before claiming complete delivery.

For the fully migrated pinned database, use the installed writer behavior when validating a moving cutoff. Creation time is assigned at the write statement and position at the wall clock. The API still omits positions and may apply a stricter retention floor than the request. [[evidence/phase-2-lifecycle-events#writer-timestamps-in-the-fully-migrated-pinned-deployment]] records what the Railway verification environment establishes and where collection must fail closed.

The SDK provisioning packages expose an organization-scoped historical reader with explicit time bounds. See [[evidence/phase-6-audit-delivery#zitadel-historical-event-reader]] for its API and observed behavior. The PostgreSQL collector commits a certified historical window with its retained audit and delivery rows using an explicit tenant mapping. [[evidence/phase-6-audit-delivery#durable-historical-collection]] records the transaction and failure tests.

## Account-switch acceptance scenarios

The approved account model changes no serialized principal format: issuer/subject remains canonical. Different directory accounts remain different principals even with identical email addresses. Memberships are not copied, combined or implicitly granted during login.

| Scenario | Required SDK behavior |
| --- | --- |
| Acme SAML account signs into Globex OIDC as a different subject | Verify the new provider evidence, atomically replace the local identity-bound session, and evaluate the new subject’s own memberships |
| New subject has no target workspace membership | No protected access or inherited roles; any authenticated account session carries only the new principal |
| Old session, discovery cursor or unfinished login returns after replacement | Reject old identity/generation-bound authority; never retarget an existing operation to the new principal |
| Same issuer/subject enters another product sharing its organization | Use that product’s audience and independently granted memberships; sign-in alone grants nothing |
| New account is disabled or provider evidence fails | Deny; a failed switch cannot relabel the old account as the target identity |
| Browser sends equal email or old role hints | Ignore them as identity-linking or authorization evidence |

```mermaid
sequenceDiagram
  participant B as Browser
  participant S as Product SDK
  participant Z as ZITADEL
  participant DB as Session and audit database
  participant F as OpenFGA
  B->>S: Switch to Globex
  S->>Z: Authenticate against Globex provider policy
  Z-->>S: Verified Globex subject B evidence
  S->>DB: Atomically replace subject A context with B and record audit
  S->>F: Check subject B membership in Globex for this product
  F-->>S: Allow or deny
  S->>DB: Persist decision audit
  S-->>B: Access as B or denial, no authority inherited from A
```

The shared live-derived fixtures already reject prior-account evidence in both TS and Go. Actual session replacement, stale-generation races, membership enforcement and durable audit must pass the Phase 4/6 runtime tests; the account-model approval does not claim these are implemented.
