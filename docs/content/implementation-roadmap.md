---
title: Implementation roadmap
date: 2026-11-08
---

The [GitHub roadmap issue #29](https://github.com/techsavvyash/heimdall/issues/29) tracks delivery through 26 focused tickets. Each ticket includes a PRD, requirements, implementation details, dependencies, boundaries and acceptance criteria. GitHub issues are the source of execution status; this page is the navigation index.

Follow [[implementation-phases|the seven sequential phases and completion gates]]. Product decisions are locked; Phase 1 encodes them as executable contracts. Audit persistence precedes protected operations, and enterprise capabilities are verified early. Both TypeScript and Go must satisfy the same behavioral contract before the initial release. SSO, SCIM user lifecycle and SIEM are required in the first release. [[design/built-in-adapters|Optional PostgreSQL adapters]] are selected scope; #51 closes the design decision while #52–#55 implement and validate it.

## Contract and service foundations

| Ticket | Prerequisites |
| --- | --- |
| [#30 — Approve the v0.1 product, security and SDK contract](https://github.com/techsavvyash/heimdall/issues/30) | Decision entry point |
| [#31 — Define portable schemas and shared TS/Go conformance fixtures](https://github.com/techsavvyash/heimdall/issues/31) | [#30](https://github.com/techsavvyash/heimdall/issues/30) |
| [#32 — Validate the proposed architecture against pinned ZITADEL and OpenFGA](https://github.com/techsavvyash/heimdall/issues/32) | [#30](https://github.com/techsavvyash/heimdall/issues/30) |
| [#33 — Implement the membership-gated tenant RBAC preset](https://github.com/techsavvyash/heimdall/issues/33) | [#31](https://github.com/techsavvyash/heimdall/issues/31), [#32](https://github.com/techsavvyash/heimdall/issues/32) |

## Core runtime SDKs

| Ticket | Prerequisites |
| --- | --- |
| [#34 — Implement runtime configuration and verified identity in TS and Go](https://github.com/techsavvyash/heimdall/issues/34) | [#31](https://github.com/techsavvyash/heimdall/issues/31), [#32](https://github.com/techsavvyash/heimdall/issues/32) |
| [#35 — Implement tenant permission checks and effective role resolution](https://github.com/techsavvyash/heimdall/issues/35) | [#33](https://github.com/techsavvyash/heimdall/issues/33), [#34](https://github.com/techsavvyash/heimdall/issues/34) |
| [#36 — Implement browser sessions and the selected TS/Go HTTP adapters](https://github.com/techsavvyash/heimdall/issues/36) | [#30](https://github.com/techsavvyash/heimdall/issues/30), [#31](https://github.com/techsavvyash/heimdall/issues/31), [#34](https://github.com/techsavvyash/heimdall/issues/34) |
| [#37 — Implement bounded workspace discovery and authenticated cursors](https://github.com/techsavvyash/heimdall/issues/37) | [#31](https://github.com/techsavvyash/heimdall/issues/31), [#35](https://github.com/techsavvyash/heimdall/issues/35) |

## Provisioning and tenant lifecycle

| Ticket | Prerequisites |
| --- | --- |
| [#38 — Implement reusable product provisioning and a thin setup CLI](https://github.com/techsavvyash/heimdall/issues/38) | [#31](https://github.com/techsavvyash/heimdall/issues/31), [#32](https://github.com/techsavvyash/heimdall/issues/32), [#33](https://github.com/techsavvyash/heimdall/issues/33) |
| [#39 — Implement retry-safe initial-owner setup and the product onboarding recipe](https://github.com/techsavvyash/heimdall/issues/39) | [#31](https://github.com/techsavvyash/heimdall/issues/31), [#35](https://github.com/techsavvyash/heimdall/issues/35), [#38](https://github.com/techsavvyash/heimdall/issues/38) |
| [#40 — Implement membership administration, invitations and ownership transfer](https://github.com/techsavvyash/heimdall/issues/40) | [#35](https://github.com/techsavvyash/heimdall/issues/35), [#39](https://github.com/techsavvyash/heimdall/issues/39) |

## Reference products and integration validation

| Ticket | Prerequisites |
| --- | --- |
| [#41 — Build the Ledger TypeScript reference integration](https://github.com/techsavvyash/heimdall/issues/41) | [#36](https://github.com/techsavvyash/heimdall/issues/36), [#37](https://github.com/techsavvyash/heimdall/issues/37), [#38](https://github.com/techsavvyash/heimdall/issues/38), [#39](https://github.com/techsavvyash/heimdall/issues/39), [#40](https://github.com/techsavvyash/heimdall/issues/40) |
| [#42 — Build the Support Go reference integration](https://github.com/techsavvyash/heimdall/issues/42) | [#36](https://github.com/techsavvyash/heimdall/issues/36), [#37](https://github.com/techsavvyash/heimdall/issues/37), [#38](https://github.com/techsavvyash/heimdall/issues/38), [#39](https://github.com/techsavvyash/heimdall/issues/39), [#40](https://github.com/techsavvyash/heimdall/issues/40) |
| [#43 — Automate the two-product security, concurrency and failure scenario suite](https://github.com/techsavvyash/heimdall/issues/43) | [#41](https://github.com/techsavvyash/heimdall/issues/41), [#42](https://github.com/techsavvyash/heimdall/issues/42) |

## Audit contract

| Ticket | Prerequisites |
| --- | --- |
| [#46 — Define audit events and implement TS/Go audit hooks](https://github.com/techsavvyash/heimdall/issues/46) | [#31](https://github.com/techsavvyash/heimdall/issues/31), [#35](https://github.com/techsavvyash/heimdall/issues/35), [#39](https://github.com/techsavvyash/heimdall/issues/39), [#40](https://github.com/techsavvyash/heimdall/issues/40) |

## Enterprise work — mandatory first release

| Ticket | Prerequisites |
| --- | --- |
| [#44 — Implement tenant-specific SAML/OIDC setup and enforce enterprise login policy](https://github.com/techsavvyash/heimdall/issues/44) | [#32](https://github.com/techsavvyash/heimdall/issues/32), [#36](https://github.com/techsavvyash/heimdall/issues/36), [#38](https://github.com/techsavvyash/heimdall/issues/38), [#40](https://github.com/techsavvyash/heimdall/issues/40), [#43](https://github.com/techsavvyash/heimdall/issues/43) |
| [#45 — Verify SCIM capabilities and approve directory lifecycle policy](https://github.com/techsavvyash/heimdall/issues/45) | [#32](https://github.com/techsavvyash/heimdall/issues/32), [#44](https://github.com/techsavvyash/heimdall/issues/44) |
| [#47 — Implement checkpointed directory reconciliation in both SDKs](https://github.com/techsavvyash/heimdall/issues/47) | [#45](https://github.com/techsavvyash/heimdall/issues/45), [#40](https://github.com/techsavvyash/heimdall/issues/40), [#46](https://github.com/techsavvyash/heimdall/issues/46) |
| [#48 — Demonstrate tenant-safe SIEM export with durable delivery](https://github.com/techsavvyash/heimdall/issues/48) | [#46](https://github.com/techsavvyash/heimdall/issues/46), [#44](https://github.com/techsavvyash/heimdall/issues/44) |

## Compatibility and release

| Ticket | Prerequisites |
| --- | --- |
| [#49 — Validate model upgrades, mixed SDK versions and rollback](https://github.com/techsavvyash/heimdall/issues/49) | [#38](https://github.com/techsavvyash/heimdall/issues/38), [#43](https://github.com/techsavvyash/heimdall/issues/43), [#46](https://github.com/techsavvyash/heimdall/issues/46) |
| [#50 — Package, document and release the first TS and Go SDKs](https://github.com/techsavvyash/heimdall/issues/50) | [#43](https://github.com/techsavvyash/heimdall/issues/43), [#46](https://github.com/techsavvyash/heimdall/issues/46), [#49](https://github.com/techsavvyash/heimdall/issues/49) |

## Adapter design decision

| Ticket | Prerequisites |
| --- | --- |
| [#51 — Scope built-in adapters and record the safety contract](https://github.com/techsavvyash/heimdall/issues/51) | Decision entry point |

## Adapter implementation

| Ticket | Prerequisites |
| --- | --- |
| [#52 — PostgreSQL storage and shared migrations](https://github.com/techsavvyash/heimdall/issues/52) | [#31](https://github.com/techsavvyash/heimdall/issues/31), [#51](https://github.com/techsavvyash/heimdall/issues/51) |
| [#53 — Managed workspace lifecycle and conservative recovery](https://github.com/techsavvyash/heimdall/issues/53) | [#35](https://github.com/techsavvyash/heimdall/issues/35), [#39](https://github.com/techsavvyash/heimdall/issues/39), [#40](https://github.com/techsavvyash/heimdall/issues/40), [#51](https://github.com/techsavvyash/heimdall/issues/51), [#52](https://github.com/techsavvyash/heimdall/issues/52) |
| [#54 — Managed discovery, drift repair and maintenance](https://github.com/techsavvyash/heimdall/issues/54) | [#37](https://github.com/techsavvyash/heimdall/issues/37), [#51](https://github.com/techsavvyash/heimdall/issues/51), [#52](https://github.com/techsavvyash/heimdall/issues/52), [#53](https://github.com/techsavvyash/heimdall/issues/53) |
| [#55 — Adapter failure safety, interoperability and consumer wiring](https://github.com/techsavvyash/heimdall/issues/55) | [#43](https://github.com/techsavvyash/heimdall/issues/43), [#49](https://github.com/techsavvyash/heimdall/issues/49), [#51](https://github.com/techsavvyash/heimdall/issues/51), [#52](https://github.com/techsavvyash/heimdall/issues/52), [#53](https://github.com/techsavvyash/heimdall/issues/53), [#54](https://github.com/techsavvyash/heimdall/issues/54) |

Examples #41/#42 additionally depend on #52–#54, migration validation #49 on #52, and release #50 on #44, #45, #46, #47, #48, #49 and #55. The adapter design decision is complete; these implementation tickets remain open.

## Completion rules

SDK tickets cover both languages and shared fixtures unless scoped to the single operator CLI or a language-specific example. Decision tickets close with recorded decisions and evidence; implementation tickets close with the stated behavior verified. Product persistence, retry execution and business-data scoping remain visible integration responsibilities.

Docker image builds are outside the current delivery gates. Reproducible tests against ZITADEL and OpenFGA are still required, using existing service images or configured endpoints.

See [[design/task-list|Design backlog]], [[design/project-structure|Code ownership]] and [[development-workspace|Workspace checks]] for context.

Phase 3 record-storage checkpoint: [[evidence/phase-3-postgres-records]] records encrypted TS/Go CRUD, concurrent CAS and bidirectional live PostgreSQL interoperability. Session lifecycle and durable audit composition remain open.

Phase 3 readiness checkpoint: [[evidence/phase-3-storage-readiness]] records primary-database startup compatibility checks in both SDKs, shared negative cases and transaction visibility. Public adapter construction and the deployment migration runner remain open.

Phase 3 migration checkpoint: [[evidence/phase-3-migration-runner]] adds one shared deployment command, atomic v1 installation, checksum verification and exclusive locking. Runtime credential provisioning and audit composition remain open.

The migration checkpoint now includes compiled-runner execution on Railway with verified cleanup, and a PostgreSQL wire-protocol test proving outcome_unknown after a committed response is lost.

Phase 3 audit checkpoint: [[evidence/phase-3-audit-persistence]] adds atomic event/delivery append in TS and Go, replay/conflict handling, lossless payload encoding and lost-COMMIT-response recovery evidence. Protected-operation orchestration and delivery workers remain open.

Phase 3 acknowledgement checkpoint: [[evidence/phase-3-audit-acknowledgements]] records atomic partial delivery receipts, replay timestamp preservation, quarantine/missing-route rejection and committed-response-loss recovery in both SDKs. Delivery transport and scheduling remain open.

Phase 3 pending-read checkpoint: [[evidence/phase-3-audit-pending]] records bounded due-event selection, payload/scope verification, explicit invalid candidates and cross-language decoding. Worker scheduling and quarantine remain open.

Phase 3 delivery-transition checkpoint: [[evidence/phase-3-audit-transitions]] records conditional attempt claims, monotonic retry deferral, idempotent quarantine and lost-COMMIT proof in both languages. External delivery policy and privileged replay remain open.

Phase 3 identity-session checkpoint: [[evidence/phase-3-identity-sessions]] records identity-bound encryption, atomic persistent denial/session deletion, cross-language locking and uncertain-COMMIT recovery. Provider freshness, reactivation and audited lifecycle composition remain open.

Phase 3 signature-verification checkpoint: [[evidence/phase-3-token-verification]] adds maintained-library JWT verification, bounded issuer-pinned JWKS retrieval, rotation/outage/cancellation tests and direct TS/Go verification of Railway-issued product tokens. Authoritative identity freshness and audited lifecycle composition remain open.

Phase 3 identity-freshness checkpoint: [[evidence/phase-3-identity-freshness]] adds authoritative human status, bounded positive freshness, local deny checks, distinct token/identity invalidation and late-positive race rejection in both SDKs. Durable factory/session/audit composition remains open.

Phase 3 observed-version invalidation checkpoint: [[evidence/phase-3-session-version-invalidation]] records revision/identity-bound deletion, refresh races, cross-language invalidation and lost-COMMIT replay that preserves a replacement session. Full session orchestration remains open.

Phase 3 pinned-model checkpoint: [[evidence/phase-3-model-binding]] records complete local artifact comparison, exact remote model reads and direct Railway verification in both SDKs. Runtime authorization composition, administrative writes and model migration remain open.

Phase 3 audited-decision checkpoint: [[evidence/phase-3-audited-decisions]] records enforced request/evaluation/terminal ordering, receipt verification, cancellation and lost-COMMIT refusal in both SDKs. Public identity/authorization composition and unresolved-decision recovery remain open.

Phase 3 durable-identity checkpoint: [[evidence/phase-3-durable-identity-gate]] connects verified identity status to scoped PostgreSQL denial and revision-safe session invalidation in both SDKs. Public factory/session/audit composition remains open.

Phase 3 configuration checkpoint: [[evidence/phase-3-runtime-configuration]] records bounded configuration/manifest loading, policy validation, detached snapshots and Railway model verification from those snapshots in TS and Go. Public runtime factory composition remains open.

Phase 3 discovery checkpoint: [[evidence/phase-3-oidc-discovery]] records bounded issuer-pinned metadata validation, same-origin endpoint and protocol capability gates, and direct TS/Go token verification from discovered JWKS on Railway. Public runtime and audited identity composition remain open.

Phase 3 authentication-audit checkpoint: [[evidence/phase-3-audited-authentication]] connects identity verification to durable completion receipts and post-commit freshness/expiry guards in both SDKs, with PostgreSQL identity/audit composition proof. Public runtime construction remains open.

Phase 3 bundled-validation checkpoint: [[evidence/phase-3-bundled-audit-validation]] removes the external schema-validation callback from audited authentication in both SDKs, with canonical-schema differential checks and PostgreSQL integration proof. Public runtime assembly remains open.

Phase 3 public-runtime checkpoint: [[evidence/phase-3-public-runtime]] exposes scoped constructors and audited bearer authentication in TS/Go, with startup failures, packed TS consumer proof and Railway service composition. Public adapter assembly, sessions and authorization remain open.


Phase 3 PostgreSQL runtime checkpoint: [[evidence/phase-3-postgres-runtime]] exposes scoped storage factories in both SDKs and verifies the public runtime with restricted PostgreSQL credentials, committed events/outbox rows and durable denial. Sessions, public authorization and combined live-provider/durable-storage proof remain open.


Current Phase 3 readiness review: [[evidence/phase-3-readiness-audit]] records the successful combined live-provider/PostgreSQL runtime proof and missing managed storage and operational foundations. Phase 3 remains open; no ticket or phase is closed by the checkpoint.


Phase 3 managed schema checkpoint: [[evidence/phase-3-managed-schema]] adds independent SQL metadata, scoped workspace/operation/candidate tables and database constraints, with local TS/Go driver tests and Railway SQL evidence. Managed migration/readiness integration and SDK adapters remain open.


Phase 3 managed deployment checkpoint: [[evidence/phase-3-managed-migrations]] adds explicit managed initialization after session readiness, combined TS/Go compatibility checks and read-only status modes in the existing CLI. Managed record adapters and operational foundations remain open.

Phase 3 workspace-read checkpoint: [[evidence/phase-3-workspace-reads]] adds scoped,
validated primary reads in both SDKs, with cross-language PostgreSQL and
cancellation proof. Managed writes and atomic audit composition remain next.

Phase 3 atomic managed audit checkpoint: [[evidence/phase-3-managed-audit-transactions]]
adds the shared transaction boundary for local SDK writes and audit/outbox receipts,
with rollback and confirmed COMMIT-response-loss proof in both languages. Semantic
managed mutation methods and operation deduplication remain required.

Phase 3 operation-intent checkpoint: [[evidence/phase-3-operation-intents]] adds
bounded payload parsing and complete shape/semantic validation in both SDKs,
plus journal fingerprint/identity/scope binding checks. Durable operation lookup,
idempotent creation and mutation transitions remain required.

Phase 3 operation-journal checkpoint: [[evidence/phase-3-operation-journal]] adds
actor-scoped validated primary lookup and atomic idempotent creation in both SDKs.
Live races, rollback, cross-language retry and COMMIT-loss tests pass. Managed
transitions, candidates and public orchestration remain required.

Phase 3 transition checkpoint: [[evidence/phase-3-operation-transitions]] adds
generation-checked state changes, lifecycle blocking and atomic candidate completion
in both SDKs. Candidate reads/rebuilds, membership intents and operational
foundations remain open; this does not implement remote mutation orchestration.

Phase 3 candidate-source checkpoint: [[evidence/phase-3-candidate-pages]] adds
bounded primary keyset pages with exact bigint positions, scope validation and
cross-language persistence proof. Rebuild coordination, membership intents and
operational foundations remain open; public discovery belongs to the next phase.

Phase 3 membership-intent checkpoint: [[evidence/phase-3-membership-intents]] adds
atomic reservation of existing workspaces, durable membership/ownership intent and
audit, with idempotency, concurrency and COMMIT-loss proof in both SDKs. Rebuild
coordination, recovery scanning and operational foundations remain open.

Phase 3 operation-scan checkpoint: [[evidence/phase-3-operation-scans]] adds
bounded primary maintenance queues for runnable, recovery and local completion
work. Scans do not claim or replay operations. Rebuild coordination and broader
operational foundations remain open.

Phase 3 candidate-repair checkpoint: [[evidence/phase-3-candidate-repair]] adds
revision-guarded publication of explicit membership observations, atomic audit and
serialization with managed mutations. Remote rebuild/checkpoint orchestration and
broader storage operational foundations remain open.

Phase 3 storage maintenance: [[evidence/phase-3-record-cleanup]] records bounded TS/Go expired-record cleanup, concurrency and commit-loss proof. This does not complete audit retention, sanitized export or restore qualification.

Phase 3 export foundation: [[evidence/phase-3-managed-export]] records sanitized, bounded workspace and per-workspace operation pages in both SDKs. Public authorization/audit composition and coordinated exit/restore remain required.

Phase 3 audit recovery foundation: [[evidence/phase-3-audit-recovery-schema]] records the prepared v2 decision slot and PostgreSQL race/rollback proof. Explicit upgrade/backfill and both SDKs’ resolution/recovery paths must pass before activation.

Phase 3 audit upgrade prerequisite: [[evidence/phase-3-audit-decision-binding]] records TS/Go request/outcome correlation and ambiguous-history refusal. Persistent backfill and result-release enforcement remain required.

Phase 3 audit backfill: [[evidence/phase-3-audit-backfill]] records bounded two-pass historical binding, transactional checkpoints, cross-language restart and committed-response-loss proof. Global upgrade coverage/activation and runtime resolution/recovery remain required.

Phase 3 decision writes: [[evidence/phase-3-audit-decision-writes]] records atomic request/terminal binding, competing-result rollback, cross-language completion and audited-runner late-result suppression. Upgrade activation and recovery administration/scanning remain required.
