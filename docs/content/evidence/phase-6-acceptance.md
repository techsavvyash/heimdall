---
title: Phase 6 acceptance review
---

Phase 6 remains open. The review maps the enterprise gate in [[../implementation-phases]] to the current TypeScript and Go evidence. Release qualification starts after the open checks below have evidence.

| Requirement | Current evidence |
| --- | --- |
| #44 Tenant SAML and OIDC, identity binding, switching and fallback | [[phase-6-tenant-sso-gate]] verifies Acme SAML and Globex OIDC, same-email account separation, stale callback and cursor rejection, password and guest denial, and workspace-scoped policy rotation on Railway. |
| #44 Provider setup, test login, invalid settings and recovery | [[phase-6-provider-setup]] verifies both protocols and SDKs, exact-candidate login, wrong-organization readback rejection and a committed create with a lost response. The deployed Ledger and Support settings routes also verify owner authorization before dispatch, durable dispatch claim recovery, the exact test login, atomic publication and enforcement of the new policy. |
| #45 Pinned SCIM behavior and credential boundaries | [[phase-2-scim]] records Users lifecycle and the pinned version. [[phase-2-scim-credentials]] and [[phase-6-scim-reader]] verify organization and provisioning-domain evidence. [[../design/enterprise-contract]] specifies explicit enrollment, polling, replay and the 60-second healthy observation target. SCIM Groups are outside the supported capability. |
| #47 SCIM jobs, failure recovery and replay | [[phase-6-scim-polling]] verifies durable discovery and provenance. [[phase-6-scim-deactivation]] verifies revocation from one disable across two active product sessions, failure and restart, newer cleanup after stale reactivation, and explicit approved re-enrollment. [[phase-6-directory-job]] verifies recovery after a missed observation and a federated operator path for strict workspaces. |
| #46 Mandatory audit and custom events | [[phase-3-acceptance]], [[phase-3-recovery-process-restart]], [[phase-3-managed-audit-transactions]] and [[phase-3-audited-retirement]] cover durable admission, unresolved outcomes, managed writes and retention. [[phase-4-effective-roles]] and [[phase-4-workspace-discovery]] cover decision accounting. [[phase-6-audit-delivery]] verifies reserved event protection and a live product business workflow correlated by identity, workspace and operation. |
| #48 Tenant-safe SIEM and service edits | [[phase-6-audit-delivery]] verifies ZITADEL and SDK event delivery to Elasticsearch, shared-organization mappings, sink restart and lost receipts, tenant filtering and direct OIDC configuration change metadata. It documents the direct OpenFGA audit gap. [[phase-6-siem-job]] verifies publication from both HTTP settings routes through the audit jobs to tenant-specific Elasticsearch indexes. |

## Open checks

- Re-qualify the scheduled directory jobs after adding the shared PostgreSQL policy source. The combined Railway run passed strict browser-operator cleanup and replay for both products. The separate live scheduled SCIM probe most recently timed out in Ledger. Capture the failing job stage and repeat the scheduled disable, denial, cleanup and re-enrollment checks before closing the phase.
