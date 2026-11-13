---
title: Audit retention and retry identity
date: 2026-11-12
---

Retention reclaims delivered payloads while preserving immutable event IDs,
receipts and authorization history. Both SDKs implement eligibility discovery,
compact identity storage and an authorized atomic retirement command.
[[evidence/phase-3-audited-retirement]] · [[evidence/phase-3-maintenance-authorization]]

## Selection and age

An invocation targets exactly one product, environment and tenant. A null tenant
means product-level records, never every tenant. The caller needs privileged
scope authorization. A decision is one indivisible group: request plus its normal
terminal, or request plus unknown terminal plus recovery administration. Pending
requests cannot be purged. Ordinary events form single-event groups.

The default period is 30 days from the latest recorded or delivered timestamp
across the whole group. Late SIEM acknowledgement therefore starts a fresh local
retention period. Zero configured sinks uses event time. Future timestamps retain
data; clock regression cannot shorten the period. A positive deployment policy
may change the duration only through authorized, audited configuration.

```mermaid
flowchart TD
  Scope[Exact product environment and tenant] --> Validate[Validate full group and original routing]
  Validate --> Protected{Held unresolved undelivered or retry protected}
  Protected -->|yes| Keep[Keep the complete group]
  Protected -->|no| Age{Retention age reached}
  Age -->|no| Keep
  Age -->|yes| Candidate[Candidate only]
  subgraph Retirement[Authorized retirement transaction]
    Candidate --> Lock[Lock and revalidate group policy and holds]
    Lock --> Audit[Append required administrative audit]
    Audit --> Reclaim[Remove payloads and update accounting]
    Reclaim --> Commit[Commit with immutable receipts preserved]
  end
```

All metadata must validate before returning any retention reason, even when the
group is held. Missing group members, cross-scope members, duplicate event/sink
IDs, unknown states, missing routing metadata or incoherent delivery timestamps
are errors. A quarantined administration delivery protects the entire recovered
decision. Lease expiry is not proof that a hold or retry obligation has cleared.

## Retry identity survives payload retirement

The existing contract returns the original receipt for identical retries and
conflicts on changed payloads under the same scoped ID. Deleting all traces would
silently allow reuse of old event and decision IDs. The retirement transaction
must preserve compact immutable identity: event ID, scope, recorded time, digest,
original routing and decision links. Exact retries still validate their supplied
full events against that identity; they cannot create new deliveries or repeat
an old business operation.

A compact receipt is not an exportable payload or a fresh authorization result.
Historical payload reads must explicitly report local unavailability after
retirement. New holds cannot recreate already-retired content. Compact identities
still consume storage and need explicit count/byte budgets and reservations.
Retirement moves accounting; it does not refund bytes that still exist. Metadata
capacity exhaustion blocks new work instead of silently forgetting IDs. Any
future removal of retry identity requires a separately reviewed retry contract.

This trades some persistent metadata for unchanged retry semantics while removing
full personal payloads after their obligations expire. It avoids an unbounded,
unmetered deduplication cache. SIEM retention remains independently configured.

## Evidence and remaining work

The shared `audit-retention.json` fixture covers 34 cases in both SDKs: age
boundaries, delayed/future/regressed timestamps, absent sink routing, holds,
unresolved/retry-protected groups, poisoned delivery, scope isolation, corruption,
group completeness and safe-integer limits. TypeScript additionally checks the
default and noninteger inputs. Classification is pure and leaves input unchanged.

A candidate grants no deletion authority. Prepared persistence now includes compact
identities, holds, metadata quotas and marker-aware inspection. Explicit retry
obligations use immutable `retry`-purpose holds in that same ledger; see
[[evidence/phase-3-retry-holds]]. A retry hold protects its whole decision and has
no lease or time-based expiry. Its explicit audited release must commit before
retirement; releasing one obligation leaves other obligations active.

Public bearer storage supports activated v4 and v5 with retained audit writes.
The product schedules bounded discovery and persists the original proposal before
dispatch. An empty scan page can still advance the cursor. The authorized command
rechecks identity, policy and current retention protections before removing
payloads. Production policy setup and scale qualification remain open.
[[evidence/phase-3-public-retention]] · [[evidence/phase-3-retirement-discovery]]
The detailed implementation protocol is `contracts/audit-retention.md` in the
repository. [[audit-contract]] remains the product audit contract, and
[[evidence/phase-3-readiness-audit]] tracks the incomplete Phase 3 gate.
