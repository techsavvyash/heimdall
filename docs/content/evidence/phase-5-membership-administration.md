---
title: Phase 5 managed membership administration
date: 2026-11-13
---

TypeScript exposes `tenant(id).memberships.add`, `assignRoles` and `remove`. Go exposes the same operations through `Tenant(id).Memberships()`. The product supplies the verified administrator, the recipient's issuer and subject, and a stable idempotency key. It resolves the intended human recipient before calling the SDK. Add creates membership with the supplied initial roles, assign replaces an existing member's role set, and remove deletes membership and direct roles. Owner changes use the dedicated ownership workflow.

```mermaid
sequenceDiagram
  participant P as Product backend
  participant H as Heimdall SDK
  participant D as Managed PostgreSQL
  participant F as Pinned OpenFGA store
  P->>H: Change membership with verified administrator and key
  H->>D: Load workspace revision and check the lifecycle
  H->>F: Check current administration and role delegation
  H->>D: Reserve workspace revision and audit intent
  H->>D: Commit dispatch generation and audit
  H->>F: Apply direct membership and role changes
  alt Definitive response and verified postconditions
    H->>D: Confirm and complete with candidate update
    H-->>P: Confirmed operation
  else Uncertain response
    H->>D: Preserve the mutation block
    H-->>P: Pending with recovery required
  end
```

The SDK requires active ownership or membership-gated delegation and checks each changed role against the manifest. It protects all owner assignments from ordinary membership mutations. The workspace revision prevents dispatch from a snapshot superseded by another managed mutation. OpenFGA receives a combined write when the changes fit one request. Larger changes use bounded batches within the same blocked workspace. Completion updates discovery in the local transaction. The runtime role needs delete access to `heimdall_managed.membership_candidates` in addition to the onboarding grants.

Retry the same command with its original key and input to inspect the result or finish durable confirmation. A reused key with changed input conflicts. An uncertain write keeps the workspace blocked and prevents conflicting mutations. See [[phase-5-membership-recovery]] for operator recovery of membership writes.

Run `heimdall-records-281fd3a0aed447028928831d90da702c` passed on PostgreSQL 17.11. The shared PostgreSQL scenario exercises both public SDKs with controlled OpenFGA responses. It verifies add, idempotency, role replacement, owner protection, removal and candidate cleanup, and the blocked gate after a committed write loses its response. The pinned-service run below extends this coverage to the live provider.

## Pinned-service verification

Run `heimdall-records-7311a10dcc554ccc8ce635a016b1d183` passed the shared TypeScript and Go administration matrix against OpenFGA 1.22.0 and PostgreSQL 17.11. Each SDK used its own store and the checked-in Ledger model. The suite exercises member addition, role replacement, removal, ownership transfer, and recovery with injected request and response failures. It verifies recovery write counts, the workspace gate, discovery candidates, and durable audit. The transfer faults verify that at least one owner survives both interrupted stages.

```mermaid
flowchart LR
  Actor[Fixture identity] --> SDK[Public TypeScript or Go SDK]
  SDK --> Journal[Managed PostgreSQL journal and audit]
  SDK --> Fault[Request and response fault harness]
  Fault --> FGA[Pinned OpenFGA store]
  FGA --> PG[OpenFGA PostgreSQL]
```

Run `HEIMDALL_PROBE_PINNED_ADMIN=1 python3 tests/integration/postgres-records.py` from the repository root with Node, pnpm, Go, Docker Compose, and a free loopback port 8080. The probe uses fixture identities and operator evidence callbacks. The earlier [[phase-5-workspace-recovery|workspace creation drill]] exercises actual service stop, drain and credential retirement. [[phase-5-reference-administration]] records the two-product HTTP verification. Invitation acceptance remains open.
