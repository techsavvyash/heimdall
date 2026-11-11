---
title: Phase 3 marker-aware TS and Go inspection
date: 2026-11-12
---

Both internal PostgreSQL adapters now inspect retained event history using one
primary snapshot. They distinguish a live event, intentional retirement, unknown
IDs and corrupt missing history without granting a new authorization decision.

```mermaid
flowchart TD
  Read[Exact tenant and event ID] --> Snapshot[Primary readiness and one SQL snapshot]
  Snapshot --> Identity{Retained identity exists}
  Identity -->|No identity or payload| Absent[No record]
  Identity -->|Payload but no identity| Error[Audit unavailable]
  Identity -->|Yes| Payload{Live payload exists}
  Payload -->|Yes| Validate[Validate envelope digest routing and member links]
  Validate --> Live[Live event and immutable identity]
  Payload -->|No| Marker{Matching complete retirement marker}
  Marker -->|Yes and all payloads absent| Retired[Retired identity without payload]
  Marker -->|No or inconsistent| Error
```

The shared `retained-inspection.json` cases run in both SDKs against clones of the
live retirement test database: ordinary-event retirement, live completion,
unknown ID and another tenant. Additional cases delete an unmarked ordinary
payload, corrupt a live event digest, cancel before I/O and mark the schema as
migrating. Missing/corrupt history must fail; migration must refuse reads.

Pinned PostgreSQL 17.11 run
`heimdall-capacity-backfill-88eb4bcdda114a328159aa7bc7219b95` passed both SDK
inspection suites and the surrounding migration, capacity and recovery harness.
Its disposable container was removed. The full workspace gate passed.

These internal reads require the prepared schemas and existing v3 primary readiness.
They do not activate retention or alter public SDK APIs. The live case validates
the original event; the retired case returns metadata only and cannot reconstruct
the personal payload or an old allow result. Public payload-only reads must later
map intentional retirement to not_found after authorization. Full retired-decision
qualification, runtime receipt retries and v4 admission remain unfinished. See
[[phase-3-retirement-markers]], [[design/audit-retention]] and
[[phase-3-readiness-audit]].
