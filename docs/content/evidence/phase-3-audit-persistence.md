---
title: Phase 3 atomic audit persistence
date: 2026-11-11
---

Internal TS and Go audit stores now persist an event and its initial delivery records in one PostgreSQL transaction. This advances #46 and #52; the public adapter and protected-operation audit flow remain incomplete.

```mermaid
sequenceDiagram
  participant SDK as Validated SDK operation
  participant Store as PostgreSQL audit primitive
  participant DB as Application database
  SDK->>Store: Immutable event and cancellation context
  Store->>Store: Snapshot, validate scope/schema/semantics, digest
  Store->>DB: BEGIN READ COMMITTED; synchronous_commit on
  Store->>DB: INSERT event ON CONFLICT DO NOTHING
  Store->>DB: Read stored event in new statement snapshot
  alt Same ID with different payload
    Store->>DB: ROLLBACK
    Store-->>SDK: Conflict; no receipt
  else New event
    Store->>DB: INSERT each initial sink delivery
  else Identical replay
    Store->>Store: Preserve existing delivery routing
  end
  Store->>DB: COMMIT
  alt Confirmed response
    Store-->>SDK: Scope, event ID, timestamp, digest receipt
  else Response lost
    Store-->>SDK: Outcome unknown; no success receipt
  end
```

Schema and semantic validation are mandatory injected dependencies of these internal primitives; the eventual SDK factory supplies them. Tests use the shared JSON schema and existing semantic validators. Verified actor identity and trusted event origin must still come from the authenticated SDK call path. This is not an API allowing arbitrary product code to impersonate mandatory events.

## Encoding and retries

The event JSONB column holds an `ha1` storage envelope with product, environment, event ID, timestamp and a base64url UTF-8 JSON payload. The digest covers the original decoded event using the shared typed encoding. This preserves contract-valid U+0000 and Unicode strings that cannot be stored directly in JSONB. Full event contents require decoding for analytics/export; SQL can inspect routing metadata.

An identical replay returns the original receipt. A different digest for the same scoped event ID conflicts. A retry does not add deliveries for newly configured sinks: it preserves the routing chosen by the first append. Explicit historical replay/export is separate. Stored payload corruption fails closed.

The transaction forces synchronous_commit on. That setting alone does not verify filesystem fsync, replication policy or crash recovery; deployment durability qualification remains required. No UPDATE privilege on immutable event rows is needed.

## Live evidence

Pinned PostgreSQL 17.11 run `heimdall-records-6ce411fddb394dd481dddeff7b2312e5` passed:

| Assertion | Observed result |
| --- | --- |
| Eight concurrent identical appends per language | One event and exactly one delivery per configured sink |
| Same ID with changed action | Conflict |
| Second delivery insert rejected by test constraint | No event or delivery rows committed |
| Wrong product or invalid envelope | Rejected before persistence |
| Already cancelled call | Cancelled; no receipt |
| Changed stored envelope | audit_unavailable |
| Go replays TS event; TS replays Go event | Matching receipt digest and unchanged initial sink routing |
| Shared event with U+0000 and Unicode | TS write and Go replay succeed losslessly |
| Runtime role with SELECT/INSERT only | Both language append paths succeed |
| Server commits, proxy drops all COMMIT response frames | Both SDKs return outcome_unknown |
| Direct read before retry after response loss | Both sink records already exist; replay recovers receipt without extra routing |

The fault test exposed an unhandled PostgreSQL client error in TS. The shared transport now observes errors while a connection is checked out, destroys a failed connection, and classifies the failure. The rerun completed without uncaught errors. Pool owners still need idle-pool error monitoring; the public factory will provide the selected integration.

The full harness also reran migrations, storage readiness, record encryption, CAS races and cross-language reads. It removed its disposable container and databases. These are local live-driver proofs; previous checkpoints separately tested the Railway schema and deployment runner.

## Remaining work

Public factory construction, schema packaging, identity denial, audit-before-allow orchestration, bounded storage/backpressure, unresolved-decision recovery, delivery acknowledgement/retry/quarantine, retention and deployment durability proof remain open. No SIEM delivery or complete authorization API is claimed at this checkpoint.
