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
| Managed workspace, operation and candidate-index transactional storage (#52) | [[phase-3-managed-schema]] adds a separate checksum-pinned physical schema and SQL proofs | [[phase-3-managed-migrations]] adds migration/readiness; [[phase-3-workspace-reads]] adds validated primary reads; [[phase-3-managed-audit-transactions]] adds the shared local audit transaction boundary; [[phase-3-operation-intents]] adds full payload and journal-binding validation; [[phase-3-operation-journal]] adds actor-scoped reads and atomic idempotent creation; [[phase-3-operation-transitions]] adds generation-checked transitions and atomic candidate completion; [[phase-3-candidate-pages]] adds bounded primary candidate pages; [[phase-3-membership-intents]] adds atomic revision-checked membership reservations; [[phase-3-operation-scans]] adds read-only maintenance queues; [[phase-3-candidate-repair]] adds revision-guarded repair publication; complete remote rebuild and checkpoint orchestration remain unproved |
| Explicit deployment migration and status commands (#52) | [[phase-3-managed-migrations]] adds explicit session/managed initialization and read-only status modes | Explicit v2 activation and mixed-schema deployment are verified; broader operational/scale qualification remains open |
| Expand/contract, sanitized export, bounded cleanup and restore procedures (#52) | Compatibility fixtures/readiness and initial migration fault tests | [[phase-3-record-cleanup]] adds bounded physical session/login expiry cleanup; [[phase-3-managed-export]] adds column-restricted sanitized export ports; [[phase-3-retained-export]] adds v4 same-snapshot export admission with restricted-column proof; [[phase-3-audited-managed-export]] adds authorized page release with durable completion; consistent exit inventory orchestration and complete restore proof remain open; initial-install tests are insufficient |
| Atomic audit/outbox append, receipt/idempotency/conflict (#46 foundation) | [[phase-3-audit-persistence]], [[phase-3-audited-decisions]], [[phase-3-audited-authentication]] | Implemented primitives and commit-loss proof exist |
| Required local audit failure prevents result release (#46 foundation) | Audited decision/authentication tests and lost-COMMIT regression | Implemented primitive behavior; every future public decision/mutation must compose it |
| Unresolved decision recovery and storage backpressure (#46) | Requests persist before evaluation; [[phase-3-audit-recovery-schema]] adds a prepared atomic resolution schema with SQL race proof; [[phase-3-audit-decision-binding]] adds shared history correlation; [[phase-3-audit-backfill]] adds transactional two-pass backfill and cross-language restart proof; [[phase-3-audit-decision-writes]] adds atomic normal request/completion ports and late-result suppression proof; [[phase-3-audit-decision-scan]] adds bounded validated pending scans; [[phase-3-audit-recovery-write]] adds atomic recovery/administration and late-result race proof; [[phase-3-audit-upgrade-prepare]] adds explicit atomic offline DDL preparation; [[phase-3-audit-upgrade-backfill]] adds automatic scope registration, bounded orchestration and checkpoint coverage checks; [[phase-3-audit-upgrade-schema]] adds pinned structural catalog validation and drift refusal; [[phase-3-audit-activation]] adds atomic activation and explicit TS/Go v2 compatibility; [[phase-3-public-audit-v2]] adds public decision-aware audit admission and mixed audit-v2/managed-v1 readiness; [[phase-3-audit-capacity-contract]] adds shared admission/reservation arithmetic; [[phase-3-audit-capacity-schema]] adds prepared atomic physical accounting and race proof; [[phase-3-audit-capacity-writes]] adds TS/Go internal transactions, cross-language recovery and capacity COMMIT-loss proof; [[phase-3-audit-capacity-upgrade]] adds explicit offline preparation, catalog validation and resolved-history import constraints; [[phase-3-audit-capacity-backfill]] adds bounded semantic accounting, durable checkpoints and capacity preflight; [[phase-3-audit-capacity-activation]] adds the internal atomic publication transaction; [[phase-3-capacity-v3-deployment]] adds activation CLI, catalog and TS/Go readiness; [[phase-3-public-capacity]] adds public v3 admission and accounting in both SDKs; [[phase-3-runtime-recovery]] adds v3 admission to internal recovery in both SDKs; [[phase-3-recovery-proposals]] adds shared immutable recovery proposals and intent fingerprints; [[phase-3-recovery-resolution]] adds validated primary resolution reads and lost-response observation; [[phase-3-recovery-maintenance]] adds internal one-page recovery using durable decision slots; [[phase-3-recovery-process-restart]] proves SIGKILL between candidates and restart in the other language; [[design/audit-retention]] adds the shared eligibility classifier and required immutable receipt-retention protocol; [[phase-3-retention-identities]] adds prepared persistent receipt/decision identity constraints; [[phase-3-retention-validation]] adds TS/Go full-history identity construction; [[phase-3-retention-persistence]] adds internal atomic PostgreSQL group copies; [[phase-3-retention-commit-loss]] proves reciprocal lost-response retries; [[phase-3-retention-holds]] adds prepared immutable hold history; [[phase-3-retention-capacity]] adds prepared metadata quotas and recovery reservations; [[phase-3-retirement-markers]] adds prepared atomic removal markers; [[phase-3-retirement-races]] tests ordinary and every completed/recovered decision member in both hold-versus-removal commit orders; [[phase-3-retained-inspection]] adds internal marker-aware TS/Go reads; [[phase-3-retired-decisions]] proves complete group removal and member inspection; [[phase-3-retained-retries]] adds internal original-receipt resolution; [[phase-3-retry-holds]] adds durable explicit retry protection; [[phase-3-retention-eligibility]] adds validated single-snapshot classification; [[phase-3-retained-writes]] adds internal atomic normal writes and retained receipts with reciprocal COMMIT-loss/accounting proof; [[phase-3-retained-recovery]] adds atomic retained recovery with reciprocal COMMIT-loss proof; [[phase-3-retention-upgrade]] adds atomic offline preparation and quota preflight; [[phase-3-retention-backfill]] adds bounded full-group copying and durable progress; [[phase-3-retention-activation]] adds independent read-only activation and shared v4 readiness; [[phase-3-retained-runtime]] adds restricted internal v4 append/recovery/read admission with runtime-role proof; [[phase-3-public-retention]] adds public bearer factories and identity admission on v4; [[phase-3-retained-maintenance]] adds retained history resolution and bounded v4 recovery maintenance; [[phase-3-retained-managed-transactions]] adds the internal atomic retained audit callback boundary; [[phase-3-retained-operations]] adds internal v4 operation factories with same-snapshot read/scan readiness and atomic retained mutations; [[phase-3-retained-managed-reads]] adds the matching workspace and candidate read factories; [[phase-3-hold-administration]] adds shared full-event hold binding; [[phase-3-atomic-holds]] adds internal atomic hold transitions and v5 audited releases at full quota in both SDKs, with authorized orchestration still open; [[phase-3-audited-retirement]] adds the privileged atomic payload removal port with cross-language receipt proof; [[phase-3-maintenance-authorization]] adds audited exact-scope authorization, a final identity gate and bound hold/retirement commands; [[phase-3-retirement-discovery]] adds bounded exact-scope candidate discovery; production policy setup, scheduling integration and scale qualification remain open | Incomplete; cannot claim production audit lifecycle from append tests |
| Deployment-wide encryption usage limits and rotation | [[phase-3-key-usage-ledger]] adds internal shared accounting, monotonic limits, retirement and TS/Go concurrency/COMMIT-loss evidence | [[phase-3-metered-records]] adds reservation-before-encryption to all PostgreSQL record writers; [[phase-3-key-management-cli]] adds explicit initialization, registration and retirement tooling; [[phase-3-key-rotation-restore]] proves two-product rolling keys and isolated record-layer restore; full-system restore and operational qualification remain unproved |

Evidence pages describe exactly which parts use fixtures, local PostgreSQL or live
Railway services. Their historical test counts are checkpoint observations, not
claims that all later requirements were already met. Implementation filenames may
differ from early ticket sketches; required behavior does not.

## Connection deadline correction

Pinned Go lib/pq 1.12.3 can block in startup after TCP connect when
`connect_timeout` is unset, despite the operation context deadline. The local
recovery maintenance fault run exposed this through a one-connection proxy that
left a subsequent connection unserviced. That run was stopped; it is not passing
reconnection evidence. Bounded connection establishment and a stalled-handshake
regression were required before Phase 3 acceptance. [[phase-3-go-connection-deadlines]]
now provides that fix and proof for Go postgres.NewConnector and both deployment
commands. Custom caller-owned pools must independently satisfy the same property;
NewStorage cannot retrofit their drivers. Separate-connection COMMIT-loss proof
alone does not establish it.

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
