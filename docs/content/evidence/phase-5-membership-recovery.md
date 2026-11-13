---
title: Phase 5 managed membership recovery
date: 2026-11-14
---

The TypeScript and Go provisioning packages expose `recoverMembership` and `RecoverMembership` for uncertain member additions, role replacements and removals. They use the same generation-bound operator verification as [[phase-5-workspace-recovery]]. The verifier receives the operation kind alongside its workspace, store, model and generation. The maintenance process keeps the old writers drained through completion.

```mermaid
sequenceDiagram
  participant O as Operator command
  participant H as Heimdall SDK
  participant D as Managed PostgreSQL
  participant F as OpenFGA
  O->>H: Recover a membership operation at its current generation
  H->>D: Read the blocked operation and immutable intent
  H->>O: Verify authorization and deployment quiescence
  O-->>H: Operator ID and evidence reference
  H->>D: Commit a new dispatch generation and audit
  H->>F: Read direct membership and role tuples
  opt Changes still missing
    H->>F: Apply the remaining changes from the recorded intent
  end
  H->>F: Verify the complete membership and role state
  H->>D: Confirm and atomically update discovery and the gate
```

The journal binds the original target and role set. Recovery can finish a previously authorized intent even if that mutation removed the administrator's own delegation. The operator verifier authorizes recovery for the current generation. The SDK refuses to modify an owner and refuses role replacement if the target membership has disappeared. A failed recovery claim or a failed postcondition keeps the gate closed. A workspace that was suspended remains suspended after completion.

PostgreSQL 17.11 run `heimdall-records-07ae3ff61c6643ebad05abfe82ce2402` passed. The shared tests cover an already-committed add, an interrupted role replacement and an already-committed removal. They verify missing-proof denial, stale-generation rejection, unchanged write counts for verified grants and revocations, one repair write for the interrupted role replacement, candidate index updates, and durable recovery audit. These tests use real PostgreSQL with controlled provider responses. See [[phase-5-membership-administration#pinned-service-verification]] for the pinned-provider repeat. [[phase-5-reference-administration]] records the two-product HTTP verification. [[phase-5-reference-invitations]] records subsequent invitation verification.
