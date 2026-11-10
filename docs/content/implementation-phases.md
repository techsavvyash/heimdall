---
title: Implementation phases and completion gates
date: 2026-11-11
---

The end goal is the complete [[design/system-design|selected Heimdall design]], implemented in TypeScript and Go, tested and deployed. The user authorized sequential execution on November 11, 2026. Work advances only after the current phase passes its gate. A partial commit or successful documentation deployment does not complete a phase.

## Execution rules

For every phase: implement its agreed scope, run focused regression/conformance tests and the full workspace gate, commit and push intended changes, verify CI, deploy the applicable artifacts to the dedicated Heimdall Railway project, verify the deployment, and commit a sanitized evidence record. Record tested source commit, commands, results, Railway deployment ID/URL and remaining limitations. Never commit credentials or customer data.

Early phases publish contracts and evidence through the Railway docs site. Phase 2 additionally deploys the pinned test stack. Later phases deploy the evolving reference products and run live checks against them. An SDK is a package embedded in those products, not a standalone Railway wrapper service.

Security or service-capability failures keep the current phase open. Fixes to an earlier phase require revalidation. Do not silently defer SSO, SCIM, SIEM, managed recovery or either language to satisfy a gate. Development milestones are not first-release claims.

## Seven phases

| Phase | Scope and tickets | Completion proof | Railway artifact |
| --- | --- | --- | --- |
| 1 — Executable contracts | #31: schemas, canonical identity/configuration/error formats, session/cursor/operation/audit contracts and shared fixtures | Both languages run positive/negative vectors; compatibility and uncertain outcomes specified; deliberate fixture mutation detected | Docs with versioned contracts and evidence |
| 2 — Pinned service verification | #32, verification portion of #45: service versions, two products/four workspaces, audiences, credential boundaries, model and enterprise checks | Reproducible live evidence including strict SSO, disable/delete visibility and recovery controls | Dedicated test services and evidence docs |
| 3 — Persistence and security foundations | #33, #34, #52, foundation of #46: model, identity, migrations, atomic storage and audit/outbox | Isolation, expiry, CAS races, schema compatibility, durable receipts and TS/Go interoperability | Test stack, migration validation and evidence |
| 4 — First working integrations | #35–#37 and initial #41/#42: sessions, Fetch/net/http, checks, roles and discovery | Login and an audited tenant-scoped operation in each language; deny/outage distinction; refresh/logout races | Ledger TS and Support Go backends |
| 5 — Provisioning and administration | #38–#40, #53/#54: setup CLI, create/status, grants, invitations, transfer, discovery and recovery | Concurrent idempotency; last-owner safety; uncertain-write quarantine and demonstrated recovery | Reference onboarding/admin flows |
| 6 — Enterprise integrations | #44–#48: SAML/OIDC enforcement, SCIM lifecycle, custom audit and SIEM | Strict tenant switching, lifecycle replay/deactivation and sink outage recovery | Enterprise test configuration and protocol receiver/verified SIEM integration |
| 7 — Release qualification | #43, #49/#50/#55 and completion of #41/#42 | Security/fault suite, mixed versions, migration/restore, clean installs and complete quickstarts; both languages pass full contract | Verified release reference deployment and evidence |

Audit persistence is built before authorization results can be released; enterprise capabilities are verified before full integration. Tickets track execution; the contracts define required behavior.

## Current phase

**Phase 1 is complete. Phase 2 is in progress.**

Phase 1 turns selected semantics into executable schemas and fixtures. Public authentication APIs remain unavailable until implemented and validated; internal contract utilities do not constitute verified authentication.

## Phase 1 acceptance

The final contract acceptance matrix and source commit/deployment proof are in
[[evidence/phase-1-acceptance]]. Both language suites, deliberate mutation detection,
full workspace checks, GitHub Actions and Railway publication passed. The final completion record was verified live before starting Phase 2.

## Evidence ledger

Phase evidence lives under docs/content/evidence/ when its first record is written. Distinguish contract checks from live-service checks, and docs deployment from runtime deployment. Each completed phase links its evidence here before the next phase begins.

- [[evidence/phase-1-progress|Phase 1 identity-encoding checkpoint]]: tests, mutation detection and Railway verification. Partial progress; no phase advancement.

- [[evidence/phase-1-errors-schemas|Phase 1 error and schema checkpoint]]: portable failures and structural conformance. Partial progress.

- [[evidence/phase-1-manifests|Phase 1 manifest checkpoint]]: product policy references, delegation and runtime binding validation. Partial progress.

- [[evidence/phase-1-session-cursor|Phase 1 session/cursor payload checkpoint]]: shared expiry and scope semantics. See the subsequent cryptographic envelope checkpoint.

- [[evidence/phase-1-crypto|Phase 1 cryptographic envelope checkpoint]]: shared authenticated formats and cross-language writer/reader vectors. Operational key management remains a runtime gate.

- [[evidence/phase-1-operations|Phase 1 operation checkpoint]]: immutable fingerprints, journal schema and conservative state transitions. Durable execution and recovery remain pending.

- [[evidence/phase-1-audit|Phase 1 audit checkpoint]]: event semantics and receipt shapes. Durable append and export remain pending.

- [[evidence/phase-1-audit-receipts|Phase 1 audit receipt checkpoint]]: portable fingerprints and scoped receipt validation. Atomic persistence and export checkpoints remain pending.

- [[evidence/phase-1-login-workspaces|Phase 1 login/workspace checkpoint]]: browser/policy binding and lifecycle-deny fixtures. Atomic claims and live enforcement remain runtime gates.

- [[evidence/phase-1-storage|Phase 1 storage compatibility checkpoint]]: migration checksums, reader/writer ranges and bridge upgrade fixtures. Live migrations and coordination remain later gates.

- [[evidence/phase-1-acceptance|Phase 1 final acceptance matrix]]: complete contract coverage, endpoint profiles, renewed mutation proof and final deployment gate.

- [[evidence/phase-2-openfga|Phase 2 OpenFGA checkpoint]]: pinned Railway services and initial live two-store probes. Phase 2 remains open.

- [[evidence/phase-2-zitadel|Phase 2 ZITADEL checkpoint]]: pinned API/Login UI, runtime credential separation and initial v2 provisioning probe. Phase 2 remains open.

- [[evidence/phase-2-scim|Phase 2 SCIM checkpoint]]: two-organization Users lifecycle and authoritative state visibility. Credential boundaries and SDK lifecycle work remain open.

- [[evidence/phase-2-scim-credentials|Phase 2 SCIM credential checkpoint]]: narrow role, organization boundaries and provisioning domains verified on Railway. Phase 2 remains open.

- [[evidence/phase-2-oidc|Phase 2 human OIDC checkpoint]]: two-product code/PKCE flow, refresh, deactivation and explicit revocation. Provider-session deletion alone allowed refresh; strict enterprise SSO remains open.
