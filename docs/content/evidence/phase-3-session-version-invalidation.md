---
title: Phase 3 observed-version session invalidation
date: 2026-11-11
---

Both internal PostgreSQL adapters now support invalidating the exact session version that produced a negative identity/token observation. This supplies the revision-safe write needed by [[phase-3-identity-freshness]]. The current request remains denied while a concurrently refreshed or recreated session is preserved.

```mermaid
sequenceDiagram
    participant Request as Request using revision 41
    participant Refresh as Concurrent refresh
    participant Store as PostgreSQL session store
    Request->>Request: Begin token status check
    Refresh->>Store: CAS revision 41 to refreshed session
    Store-->>Refresh: Committed revision 42
    Request->>Request: Receive negative result for old token
    Request->>Store: Invalidate key + identity + revision 41
    Store-->>Request: false; revision 42 preserved
    Request-->>Request: Reject this request; preserve replacement cookie
```

The delete predicate includes product, environment, session kind, opaque key hash, canonical issuer/subject hash and revision. It runs under the same scoped transaction advisory lock as create/CAS and identity invalidation. A confirmed matching deletion returns true; a nonmatching observation returns false. False can mean a replacement exists, so orchestration must never turn it into an unconditional delete or apply the old negative to a newly read revision.

If deletion wins the race, the old CAS cannot resurrect the row. If CAS wins, old-version invalidation cannot remove its replacement. Explicit logout and account-wide identity denial keep their separate semantics; an individual token observation must not silently become either operation.

## Live proof

Pinned PostgreSQL 17.11 run `heimdall-records-147e7b37686a4a14a6a0b04b43a71269` passed the complete regression harness. The shared `contracts/fixtures/session-invalidation.json` pins malformed revision cases, race count and the cross-language record key.

| Scenario | Result in both SDKs |
| --- | --- |
| Wrong issuer, subject, product or environment | No deletion |
| Old revision after successful CAS | Refreshed record preserved |
| Matching revision | Exactly that row removed |
| Repeated delete | False without another mutation |
| CAS after deletion | No resurrection |
| Old delete after recreate | New revision preserved |
| Twelve concurrent CAS/delete races per language | Exactly one winner in each race |
| Confirmed COMMIT response lost | outcome_unknown |
| Recreate before retrying ambiguous delete | Exact old retry leaves replacement intact |

Go additionally read and conditionally deleted the TS-created shared session; the TS peer pass verified its absence. Both live language paths used the restricted identity runtime role for ordinary operations. Wire-protocol fault cases independently confirmed PostgreSQL COMMIT before discarding its response; direct reads verified deletion before recreation and retry.

Machine-readable evidence is committed in `tests/integration/evidence/session-version-invalidation-2026-11-11.json`.

The full workspace gate passed: 952 TypeScript tests, Go tests with the race detector, formatting/lint/types/Knip, SDK builds and 71 Quartz pages. Optional live cases run through the dedicated PostgreSQL harness rather than the default test environment.

## Remaining work

The built-in session factory must bind this operation to the loaded identity/revision and compose required audit, cookie handling and refresh policy. Core adapters must provide equivalent semantics. Backup restore, cross-system reconciliation and complete reference-product flows remain required. Phase 3 is still in progress; this checkpoint does not claim a complete session lifecycle.
