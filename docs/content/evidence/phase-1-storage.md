---
title: Phase 1 evidence — storage version and migration compatibility
date: 2026-11-11
---

**Partial progress; Phase 1 remains open.** TS and Go now enforce the same metadata, migration history and reader/writer compatibility rules through internal read-only helpers.

## Verified behavior

Shared fixtures cover current schema, bridge SDK before/after an additive migration, new reader/writer protocol before/after migration, unsupported future/old schemas, removed reader or writer compatibility, migrating state, reversed ranges, missing/extra/duplicate/skipped ledger entries, changed checksums and invalid bundled policy.

Both languages consume the same metadata schema and semantic fixtures. The full workspace gate passes with 706 Vitest tests, Go race tests, formatting/lint/type/static checks, SDK build/declarations and Quartz. No dependencies were added.

```mermaid
flowchart LR
  Policy[Bundled SDK protocol ranges and checksums] --> Check[Read-only compatibility check]
  DB[Primary database metadata snapshot] --> Check
  Check -->|Ready supported complete matching ledger| Ready[Permit runtime startup]
  Check -->|Unknown version corrupt history or migrating| Stop[Unavailable - no automatic migration]
  Bridge[Deploy tested bridge SDK] --> Additive[Explicit locked additive migration]
  Additive --> New[Deploy next protocol]
  New --> Drain[Drain old readers and writers]
  Drain --> Cleanup[Separate incompatible cleanup]
```

## Remaining proof

The fixtures contain synthetic checksums and do not constitute SQL migrations. No database lock, DDL, backup restoration or live rolling upgrade has been executed here. Metadata is not an integrity boundary against a database administrator. Startup checks cannot fence processes already running; deployment coordination remains mandatory.

## Deployment proof

Pending verification for this checkpoint.
