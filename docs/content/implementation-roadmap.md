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
