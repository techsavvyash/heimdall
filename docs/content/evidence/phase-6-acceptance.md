---
title: Phase 6 acceptance review
---

Phase 6 remains open. The review maps the enterprise gate in [[../implementation-phases]] to the current TypeScript and Go evidence. Release qualification starts after the open checks below have evidence.

| Requirement | Current evidence |
| --- | --- |
| #44 Tenant SAML and OIDC, identity binding, switching and fallback | [[phase-6-tenant-sso-gate]] verifies Acme SAML and Globex OIDC, same-email account separation, stale callback and cursor rejection, password and guest denial, and workspace-scoped policy rotation on Railway. |
| #44 Provider setup, test login, invalid settings and recovery | [[phase-6-provider-setup]] verifies both protocols and SDKs, exact-candidate login, wrong-organization readback rejection and a committed create with a lost response. The product-owned authorization and policy publication boundary still needs the integrated check below. |
| #45 Pinned SCIM behavior and credential boundaries | [[phase-2-scim]] records Users lifecycle and the pinned version. [[phase-2-scim-credentials]] and [[phase-6-scim-reader]] verify organization and provisioning-domain evidence. [[../design/enterprise-contract]] specifies explicit enrollment, polling, replay and the 60-second healthy observation target. SCIM Groups are outside the supported capability. |
| #47 SCIM jobs, failure recovery and replay | [[phase-6-scim-polling]] verifies durable discovery and provenance. [[phase-6-scim-deactivation]] verifies revocation from one disable across two active product sessions, failure and restart, newer cleanup after stale reactivation, and explicit approved re-enrollment. [[phase-6-directory-job]] verifies recovery after a missed observation and a federated operator path for strict workspaces. |
| #46 Mandatory audit and custom events | [[phase-3-acceptance]], [[phase-3-recovery-process-restart]], [[phase-3-managed-audit-transactions]] and [[phase-3-audited-retirement]] cover durable admission, unresolved outcomes, managed writes and retention. [[phase-4-effective-roles]] and [[phase-4-workspace-discovery]] cover decision accounting. [[phase-6-audit-delivery]] verifies reserved event protection and a live product business workflow correlated by identity, workspace and operation. |
| #48 Tenant-safe SIEM and service edits | [[phase-6-audit-delivery]] verifies ZITADEL and SDK event delivery to Elasticsearch, shared-organization mappings, sink restart and lost receipts, tenant filtering and direct OIDC configuration change metadata. It documents the direct OpenFGA audit gap. [[phase-6-siem-job]] documents the runnable scheduler integration. |

## Open checks

- Exercise the complete product-owned provider settings flow with durable authorization, activation only after the candidate test and an audited policy change. The existing deployed rotation uses trusted environment configuration. The wrong-organization provider probe exercises SDK readback after setup, which does not establish that an unauthorized product administrator's request is rejected before dispatch.
- Trace a live directory transition from the provider event and local denial revision to the membership mutation in the configured SIEM. The existing SCIM evidence verifies the denial, revision and membership audit in PostgreSQL. The identity collector and product SIEM probes verify their upstream and product paths individually.
- Verify the reference jobs and deployment configuration together as part of the final enterprise gate. The operator-driven strict cleanup example covers one strict workspace per directory mapping. Scheduled deletion of stale access can proceed through the identity denial even while a strict workspace awaits a federated operator for the role cleanup.
