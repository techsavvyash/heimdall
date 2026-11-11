---
title: Phase 3 identity-bound sessions and invalidation
date: 2026-11-11
---

The internal TypeScript and Go PostgreSQL session stores now bind encrypted records to exact issuer/subject identity and serialize session writes against identity invalidation. This extends [[phase-3-postgres-records]]. Phase 3 remains in progress.

```mermaid
sequenceDiagram
    participant SDK as SDK lifecycle (composition pending)
    participant Store as Session store
    participant DB as PostgreSQL
    SDK->>Store: Create validated session payload
    Store->>DB: Begin transaction; acquire scoped identity lock
    Store->>DB: Check persistent identity denial
    alt Identity denied
        Store-->>SDK: unauthenticated
    else Identity eligible
        Store->>DB: Insert encrypted payload + bound identity hash
        Store->>DB: COMMIT
        Store-->>SDK: Record and revision
    end
    SDK->>Store: Authoritative negative identity observation
    Store->>DB: Begin transaction; acquire same identity lock
    Store->>DB: Upsert denial + delete matching scoped records
    Store->>DB: COMMIT
    Store-->>SDK: Deleted count, or outcome_unknown if response lost
```

The identity hash is derived from the canonical issuer/subject encoding, including Unicode and embedded NUL. Product and environment scope every query and lock. Email addresses are never identity keys. The encrypted associated data binds both the opaque storage key and the identity hash; reads additionally check the decoded payload identity. Generic record CRUD cannot read, mutate or delete identity-bound rows.

A session creation racing with invalidation either completes first and is deleted, or sees the persistent denial and fails. CAS cannot change the session principal. Reads exclude denied identities, but cannot retract a record already returned to an in-flight caller. Negative observation time only advances; a deleted reason cannot be downgraded by a replayed inactive event.

## Verification

Pinned PostgreSQL 17.11 run `heimdall-records-8431a2a9111e444db47b7c4a03018874` passed the complete live regression harness, including Go race detection. Machine-readable evidence is committed in `tests/integration/evidence/identity-sessions-2026-11-11.json`.

The shared fixture `contracts/fixtures/session-storage.json` pins the Unicode identity digest, signed advisory-lock key and session payload. Live tests cover bidirectional TS/Go encrypted reads, Go invalidating a TS-created session, both SDKs blocking on the independently computed lock, concurrent creation/invalidation, identity-preserving CAS, generic CRUD bypass prevention and persistent denial. TypeScript additionally checks issuer/product/environment isolation, index tampering, and a forced delete failure that rolls back both denial and deletion.

A wire proxy drops a confirmed COMMIT response in each language. Each invalidation returns outcome_unknown. An independent database read confirms the denial and absence of the session before retry. Repeating invalidation deletes zero additional records, and a fresh creation remains denied.

## Limits and remaining work

These are internal persistence primitives. The mandatory validation callback checks payload shape and binding; it does not prove provider authentication or apply idle/absolute timeout policy. Provider freshness, refresh-token revocation, local identity cache invalidation, required audit composition, session middleware and public factory construction remain open. A provider-wide negative observation must reach all relevant product bindings; this scoped store does not discover those bindings.

There is deliberately no clear-denial operation yet. Reactivation must reconcile authoritative current status, cleanup and grants and require a fresh login. Historical activation evidence cannot clear a newer inactive/deleted observation. This checkpoint does not advance to Phase 4 or claim a complete session lifecycle.
