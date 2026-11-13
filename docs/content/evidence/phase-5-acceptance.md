---
title: Phase 5 acceptance
date: 2026-11-14
---

Phase 5 covers the provisioning and administration gate in [[../implementation-phases]]. The tests use the TypeScript SDK in Ledger and the Go SDK in Support. The shared fault and storage suites also exercise the same behaviors in both languages.

| Requirement | Evidence |
| --- | --- |
| Independent, portable product provisioning and restart | The two-product setup drill verifies distinct ZITADEL audiences and clients, OpenFGA stores and model IDs, and reciprocal TS/Go resumption. Provider-specific checkpoints cover lost creation responses and preserved secrets. See [[phase-5-product-setup]], [[phase-5-zitadel-project]], [[phase-5-zitadel-application]] and [[phase-5-openfga-setup]]. |
| Actor-bound idempotent creation and scoped status | Four concurrent identical create calls allocate one workspace and perform one grant. Changed input conflicts. The reference probe verifies that Support cannot read a Ledger operation. See [[phase-5-managed-onboarding]] and [[phase-5-reference-onboarding]]. |
| Suspension, cancellation fencing and activation | Pending, suspended and blocked workspaces deny managed access. Resume requires a cleared block and a resolved journal. Pre-dispatch cancellation and stale-generation refusal are covered by the journal tests. See [[phase-5-workspace-lifecycle]] and [[phase-3-operation-transitions]]. |
| Audited delegation, invitations and last-owner safety | The shared provider fault matrix covers grants, replacement, revocation, transfer and recovery. The reference products reject stolen and expired invitations, replay the accepted receipt, and deny an outstanding invitation after loss of delegation. See [[phase-5-membership-administration]], [[phase-5-ownership-transfer]] and [[phase-5-reference-invitations]]. |
| Quarantine and safe recovery | The isolated pinned OpenFGA drill verifies stop, drain and credential retirement before recovering blocked operations. The separate Railway drill verifies the service-control sequence on the single-replica topology. See [[phase-5-workspace-recovery]], [[phase-5-membership-recovery]] and [[phase-2-railway-recovery]]. |
| Discovery and drift repair | Discovery validates cursor scope and live membership. Cross-language checkpoint resumption restores a missing candidate and removes a stale candidate after an explicit membership check. See [[phase-4-workspace-discovery]], [[phase-4-managed-access]] and [[phase-5-discovery-rebuild]]. |
| Bounded operator maintenance | The scheduler honors due times and completes durable confirmations locally. An uncertain dispatch still requires operator recovery. The audited export uses a separate reader, and operator metrics and expiry cleanup are explicit calls. See [[phase-5-operation-reconcile]] and [[phase-5-operator-tools]]. |

The evidence covers the tested single-replica recovery topology. Production writers and credentials must be drained under the deployed topology's own recovery procedure. The reference products use an operator subject and product-owned invitation delivery. Phase 6 covers enterprise enforcement and integration. Phase 7 covers migration, restore, performance and clean-install qualification.

The final Phase 5 reference run was `3cb2da11-7a56-4db6-819c-8094adc08598` on Railway. Both deployments and PostgreSQL readback passed. [[phase-5-operator-tools]] records their deployment IDs. The implementation passed `pnpm run check` and both GitHub workspace jobs. The focused setup and reference integration commands were `python3 tests/integration/setup-journal.py` and `HEIMDALL_PROBE_REFERENCE_PRODUCTS=1 python3 tests/integration/postgres-records.py` with pinned verification credentials supplied through the environment.
