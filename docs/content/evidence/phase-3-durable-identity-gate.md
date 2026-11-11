---
title: Phase 3 durable identity gate
date: 2026-11-11
---

The internal identity verifier now connects to the PostgreSQL session adapter through a product/environment-bound gate in both SDKs. This supplies the durable deny read and precise negative handling required by [[phase-3-identity-freshness]], using the storage semantics proven in [[phase-3-session-version-invalidation]].

```mermaid
sequenceDiagram
    participant SDK as Future session factory
    participant Verify as Identity verifier
    participant DB as Product PostgreSQL
    participant Z as Provider status APIs
    SDK->>DB: Load and validate session and revision
    SDK->>Verify: Token and exact internal session context
    Verify->>DB: Read scoped identity denial
    Verify->>Z: Revalidate status when freshness requires
    alt Token is inactive
        Verify->>DB: Delete only key, identity and observed revision
        DB-->>Verify: Commit or nonmatching replacement
        Verify-->>SDK: Unauthenticated, preserve newer session
    else Explicit account inactive or deleted
        Verify->>DB: Atomically persist denial and remove identity sessions
        DB-->>Verify: Committed invalidation
        Verify-->>SDK: Unauthenticated
    else Provider active and local gate allows
        Verify-->>SDK: Verified principal within freshness bounds
    end
```

Factory session loading in this diagram remains a Phase 4 integration requirement. The new gate supplies the database callbacks, not the complete cookie/session lifecycle.

## Scope and context

Gate construction rejects a product/environment mismatch. Session contexts encode a validated internal storage key and canonical positive revision. They are constructed from a loaded session, never accepted as browser headers or raw cookies. A token negative cannot remove a concurrently refreshed/recreated revision, and a false delete result does not authorize clearing its replacement cookie.

Bearer token negatives do not delete sessions or invent account tombstones. Explicit account negatives persist a scoped denial and remove identity-bound sessions atomically. Later positive provider responses cannot clear that denial. Local deny checks run even when the verifier has a cached positive; the cached-return path also checks cancellation immediately before releasing a principal.

## Evidence

Pinned PostgreSQL 17.11 run `heimdall-records-3cd56cae52c048bb87bec50e399c3fad` passed the full storage regression harness. New tests in both SDKs compose real signed-token verification, fixture provider responses and real PostgreSQL writes using the restricted identity runtime role.

| Case | Verified outcome |
| --- | --- |
| Active signed token | Principal returned after local deny check |
| Inactive bearer token | Unauthenticated, existing sessions preserved |
| Inactive session token | Only observed session revision removed |
| Refresh during status lookup | Replacement revision preserved |
| Explicit inactive identity | Durable denial and session deletion |
| Deleted observation after inactive | Durable reason upgraded to deleted |
| Other subject | Session preserved |
| Warm verifier after persistent denial | Rejected without another provider request |
| Session recreation after denial | Rejected |
| Identity or session-delete COMMIT response lost | outcome_unknown preserved through the gate |
| Cancellation at cached-return boundary | No principal released |

The combined test uses fixture provider responses, not a new end-to-end Railway login. The existing [[phase-3-identity-freshness|Railway status evidence]] remains the provider compatibility proof. Shared `contracts/fixtures/identity-context.json` pins context encoding/rejection in both languages. Machine-readable storage evidence is committed as `tests/integration/evidence/identity-gate-2026-11-11.json`.

The full workspace gate passed 1,004 TypeScript tests, Go race tests, formatting/lint/types/Knip and SDK/Quartz builds. The ordinary gate skips 24 optional live tests; the PostgreSQL harness executes the relevant live cases separately and cleans up its disposable database.

## Remaining work

This is internal persistence/identity composition. Public factories must bind immutable configuration, load/validate sessions per request, supply required durable audit, and implement refresh/logout and enterprise context. Reactivation requires explicit reconciliation; the gate has no clear-denial shortcut. Phase 3 remains in progress.
