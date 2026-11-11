---
title: Phase 3 readiness audit — incomplete
date: 2026-11-11
---

**Phase 3 is not complete.** This audit compares the current repository and live
proofs with [[implementation-phases]], tickets #33/#34/#52 and the foundation of
#46. It records concrete gaps rather than treating green primitive tests or a
docs deployment as completion. It does not change the seven-phase scope or close
any ticket. Later-phase capabilities remain mandatory in both SDKs.

## Foundation evidence matrix

| Requirement | Current evidence | Finding |
| --- | --- | --- |
| Product-defined membership-gated roles and permissions (#33) | [[phase-3-tenant-model]], [[phase-3-model-binding]] and their recorded pinned OpenFGA runs | Compiler/model foundations exist; administration workflows remain later work |
| Delegation and owner-only transitions (#33) | Shared delegation fixtures and model compiler tests | Pure delegation validation exists; remote mutation enforcement still requires Phase 5 |
| Fixed trusted config, no per-request issuer/store/model override (#34) | [[phase-3-runtime-configuration]], [[phase-3-public-runtime]] and runtime-startup fixtures | Implemented in public bearer runtime |
| Maintained-library token verification, key rotation/outage/cancellation (#34) | [[phase-3-token-verification]], [[phase-3-identity-freshness]] and shared negative tests | Primitive and live identity evidence exists |
| Public runtime with real identity/model services and durable storage | [[phase-3-postgres-runtime]] combined run | Both SDKs and both products now pass |
| Public authorization rejects fabricated principals (#34) | Public exports currently expose bearer verification only | Not yet verifiable; the Phase 4 authorization boundary is absent, so #34 cannot be closed wholesale |
| Shared immutable SQL, migration serialization and runtime no-DDL (#52) | Session-v1 checksum/catalog, [[phase-3-migration-runner]], [[phase-3-storage-readiness]] | Session/audit schema implemented and tested |
| Scoped encrypted records, expiry and non-repeating CAS (#52) | [[phase-3-postgres-records]], [[phase-3-identity-sessions]], [[phase-3-session-version-invalidation]] | Session/login primitives and cross-language storage proof exist |
| Managed workspace, operation and candidate-index transactional storage (#52) | [[phase-3-managed-schema]] adds a separate checksum-pinned physical schema and SQL proofs | [[phase-3-managed-migrations]] adds migration/readiness; [[phase-3-workspace-reads]] adds validated primary reads; managed write/operation/candidate adapters remain missing |
| Explicit deployment migration and status commands (#52) | [[phase-3-managed-migrations]] adds explicit session/managed initialization and read-only status modes | Implemented and verified; upgrade orchestration beyond v1 remains open |
| Expand/contract, sanitized export, bounded cleanup and restore procedures (#52) | Compatibility fixtures/readiness and initial migration fault tests | Operational implementation and complete restore proof are missing; initial-install tests are insufficient |
| Atomic audit/outbox append, receipt/idempotency/conflict (#46 foundation) | [[phase-3-audit-persistence]], [[phase-3-audited-decisions]], [[phase-3-audited-authentication]] | Implemented primitives and commit-loss proof exist |
| Required local audit failure prevents result release (#46 foundation) | Audited decision/authentication tests and lost-COMMIT regression | Implemented primitive behavior; every future public decision/mutation must compose it |
| Unresolved decision recovery and storage backpressure (#46) | Requests persist before evaluation; no recovery scanner or configured hard-cap enforcement exists | Incomplete; cannot claim production audit lifecycle from append tests |
| Deployment-wide encryption usage limits and rotation | Envelope fixtures and key-ID decoding exist | Cross-replica enforcement and production composition remain unproved |

Evidence pages describe exactly which parts use fixtures, local PostgreSQL or live
Railway services. Their historical test counts are checkpoint observations, not
claims that all later requirements were already met. Implementation filenames may
differ from early ticket sketches; required behavior does not.

## Work before Phase 3 acceptance

1. Complete scoped TS/Go workspace/operation/candidate adapters, atomic
   transaction boundaries, and shared TS/Go concurrency/rollback tests. Deployment
   migration and readiness for the separate managed schema are now tested. This must
   preserve quarantine of uncertain dispatched operations, not introduce an
   automatic replay or lease-fencing claim.
2. Finish deployment/storage operational foundations: bounded cleanup/export interfaces, key-usage and audit-capacity safety, and explicit
   restore/compatibility evidence. Distinguish implemented recovery controls from
   procedures that still depend on future managed lifecycle orchestration.
3. Re-audit the phase matrix against current source, test outputs, recorded
   deployment IDs and issue acceptance criteria. Keep cross-phase requirements
   open on their owning tickets; do not silently declare all of #34/#46/#52 done.
4. Commit and publish a final Phase 3 acceptance record only after its required
   foundations pass and the remaining cross-phase obligations are explicit.
   Verify Railway publication before starting Phase 4.

The next phase still owns browser sessions/HTTP adapters, permission and role APIs,
workspace discovery and the first product backends. Phase 5 owns provisioning and
managed administration/recovery workflows. Phase 6 owns strict tenant SSO, SCIM,
custom audit and SIEM integration. Phase 7 owns complete failure/compatibility,
restore and release qualification. This ordering does not waive missing
foundations or permit a first release before all seven phases pass.
