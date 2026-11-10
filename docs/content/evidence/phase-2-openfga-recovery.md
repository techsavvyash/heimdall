---
title: Phase 2 evidence — local OpenFGA quiescence faults
date: 2026-11-11
---

Phase 2 remains open. A new reproducible fault probe demonstrates a concrete quiescence sequence on the pinned local single-replica OpenFGA/PostgreSQL stack. This does not yet certify Railway recovery or the future SDK workflow.

## Executed proof

Run cc8460a0-f525-489c-944a-f7c6f2215d40 used OpenFGA 1.22.0 and PostgreSQL 17.11 from deploy/verification/compose.yaml, with the exact digests in versions.json. The fresh project, generated credentials, database and loopback fault proxy were owned by the run. tests/integration/openfga-recovery.mjs and deploy/verification/recovery.md provide reproduction; sanitized results are in tests/integration/evidence/openfga-recovery-local-2026-11-11.json.

| Fault or control | Verified observation |
| --- | --- |
| Commit followed by dropped response | Proxy saw upstream success; client saw transport failure; tuple existed |
| Dispatched write stalled inside database | PostgreSQL reported a lock wait while the test held the tuple-table lock |
| Old server stopped | Owned OpenFGA container killed; inspected running state was false |
| Outstanding database paths drained | Remaining database backends terminated; count reached zero excluding the control and test-lock connections |
| Credential retirement | Replacement container used a new key; delayed old-key write returned HTTP 401 |
| Interrupted write after restart | Tuple absent after releasing the lock and restarting |
| Previously committed write | Survived restart, then explicitly deleted using the new credential |
| Stale regrant after cleanup | Old-key request rejected; removed tuple stayed absent |
| New recovery credential | Confirmed write/read/delete/read sequence passed |
| Teardown | Unique project's containers and volume removed; label-filtered inventory empty |

```mermaid
sequenceDiagram
  participant Client as Test client
  participant FGA as Old OpenFGA process
  participant DB as PostgreSQL
  participant Operator as Recovery controls
  Client->>FGA: Write A
  FGA->>DB: Commit A
  FGA--xClient: Response deliberately dropped
  Client->>FGA: Write B
  FGA->>DB: B waits behind test lock
  Operator->>FGA: Kill and verify stopped
  Operator->>DB: Terminate remaining backends and verify drained
  Operator->>DB: Release test lock
  Operator->>FGA: Start replacement with new credential
  Client->>FGA: Delayed request with old credential
  FGA-->>Client: 401
  Operator->>FGA: Read A present, B absent; reconcile with new credential
```

## What this establishes and what it does not

The proof distinguishes work still outside the old server from work already inside its database path. Credential retirement rejects the former; stopping the server and draining database transactions resolves the latter. A timeout, lease, read or credential rotation on its own is not the demonstrated boundary.

This requires deployment-wide maintenance under one trusted operator. The test has one API replica, one preshared credential path and an isolated database, with no independent writers. A real runbook must inventory and stop every writer/replica, prevent replacements, retire every credential path and verify the database boundary. Rolling restarts or stopping only a product worker are insufficient.

The SDK's durable operation journal, quarantine gate, authorized recovery action, generation checks and atomic completion remain unimplemented. Railway controls and multi-replica deployment handling remain unverified. Until those checks pass, this local result cannot be used to clear a managed workspace in production or close Phase 2.

## Validation and publication

The complete local fault probe passed and removed its owned resources. The full workspace gate also passed: 841 Vitest tests, Go race tests, formatting/lint/types/Knip/vet, SDK build/declarations and Quartz. Source commit 6c510bb5d9b0fd579465fa2fb4d2eb15b802f13e passed GitHub Actions runs 42920470829 and 42920459099. Railway docs deployment 1365d5e6-e74f-4e68-b15b-3765df7a455d reached SUCCESS. The public page returned HTTP 200 with the fault-run ID, phase status and Mermaid source. This is publication on Railway of a local fault proof, not a Railway runtime recovery test.

Follow-up: [[phase-2-railway-recovery]] records the Railway control-plane and database-drain run for the single-replica verification topology. SDK recovery workflow requirements remain open.
