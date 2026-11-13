---
title: Phase 6 durable SCIM deactivation
date: 2026-11-14
---

The TypeScript PostgreSQL entry point exposes `createDirectoryDeactivation`. The Go PostgreSQL package exposes `NewDirectoryDeactivation`. The product scheduler calls the observer with a previously tracked issuer and subject under its trusted directory configuration. Each call reads current state through the [[phase-6-scim-reader|directory reader]].

```mermaid
sequenceDiagram
  participant W as Product scheduler
  participant S as Heimdall SDK
  participant Z as ZITADEL
  participant D as Product PostgreSQL
  W->>S: Observe tracked directory subject
  S->>Z: Verify current identity state
  Z-->>S: Inactive or missing
  S->>D: Lock the product identity
  S->>D: Record durable cleanup intent
  S->>D: Write identity denial and invalidate sessions
  S->>D: Commit the transaction
  D-->>S: Committed
  S-->>W: Return observed state and invalidation count
```

Install directory migrations `0001.sql` through `0003.sql` in order using the product's deployment migration tooling. The directory schema is installed independently of the runtime audit protocol. Grant the worker role schema and sequence usage and select, insert and update access to the deactivation table, alongside the runtime permissions for identity denial and session invalidation. The normal authorization runtime does not require access to the directory table. [Directory schema SQL](https://github.com/techsavvyash/heimdall/tree/feat/docs-website/contracts/persistence/directory)

When upgrading an existing directory worker, stop its observation loop before installing `0002.sql` and restart it with the revision-aware SDK. Existing intents receive a pending revision during migration.

One transaction persists the deactivation intent, denies the identity and invalidates its sessions. It uses the same identity lock as the session store. Product and environment scopes are independent. A retry can confirm local denial again without duplicating the intent. An existing intent cannot be rebound to another organization. Deleted status dominates inactive status on retry. An active or initial state leaves an existing denial in place.

The disposable PostgreSQL regression invokes both public SDK adapters under the restricted runtime role. It verifies that an injected session-deletion failure rolls back both the intent and the deny, and that a successful retry, an initial state, a mismatched organization and an unrelated environment preserve their expected boundaries. Reproduce with `tests/integration/postgres-records.py`.

## Recovering pending cleanup

The product scheduler can scan pending deactivations through TypeScript `createDirectoryCleanup` or Go `NewDirectoryCleanup`. These APIs bind the worker to a product, environment, issuer and organization. The first page starts at the oldest pending revision. A page returns up to 100 items, each with a subject, revision, reason and observation timestamp. Use the last revision to continue the current scan and restart from the beginning on the next run. Concurrent transactions can commit out of allocation order, so a page cursor is unsuitable as a persistent ingestion checkpoint.

```mermaid
flowchart LR
  S[Product scheduler] --> P[Read pending revisions]
  P --> W[Resume audited membership operations]
  W -->|Confirmed effects| A[Acknowledge the exact revision]
  W -->|Pending or failed effect| R[Leave work pending for retry]
  A --> D[Retain identity denial]
  X[Later confirmed deletion] --> Y[Create newer pending revision]
```

The scan does not claim work. Concurrent callers can see the same revision and must use the same idempotency keys for downstream operations. After those operations are confirmed, call `complete` in TypeScript or `Complete` in Go. Retrying the same acknowledgement succeeds. A later deletion supersedes an inactive revision, so a stale acknowledgement cannot close the new work. Acknowledgement leaves the identity denial in place.

PostgreSQL run `heimdall-records-a015b93916eb4f339d8bb6e1a6e78e7b` passed the directory tests for pending scans, cursor advancement, organization isolation, repeated acknowledgement and stale-revision refusal in both languages.

## Managed membership removal

The TypeScript `reconcile` and Go `Reconcile` methods take the pending subject and revision, a workspace list selected by the product, and an authorized administrator principal from that product runtime. Supply only workspaces where the product policy requires complete removal of the subject. The first call checks for durable identity denial and binds the sorted workspace plan and initiating principal to the revision. Calls with a different plan or principal fail with a conflict. Each workspace removal uses the existing audited, idempotent membership path. Ownership transfer remains a separate authorized operation.

```mermaid
sequenceDiagram
  participant P as Product administrator
  participant S as Directory cleanup adapter
  participant D as PostgreSQL
  participant M as Managed membership API
  P->>S: Submit workspace plan for pending revision
  S->>D: Check local denial and bind plan
  loop Each workspace in the plan
    S->>M: Remove subject with stable idempotency key
    M-->>S: Confirmed or recovery required
  end
  S->>D: Acknowledge after all removals are confirmed
```

If OpenFGA may have applied a removal and the response is lost, the SDK returns the pending membership operation and leaves directory cleanup pending. Resolve it using the existing recovery procedure and retry the same plan. Once every removal is confirmed, the SDK acknowledges the directory revision. A replay after acknowledgement returns success for the same plan.

PostgreSQL run `heimdall-records-5f9ccfdd51d04f9a847405c0fe28446f` exercised both public SDKs with a fault-injected OpenFGA fixture. The tests covered denial required before removal, a lost response after apply, a conflicting plan, retry of the original operation, recovery and final acknowledgement.

This path covers administrator-driven whole-membership removal. Grant provenance and selective cleanup, unattended scheduling authority, ingestion checkpoints, reactivation, lifecycle audit export and measured end-to-end worker latency remain part of Phase 6.
