---
title: Phase 3 deployment migration runner
date: 2026-11-11
---

The deployment-only `heimdall-migrate` command now installs the exact shared session schema v1 for both SDK languages. It runs once and exits; it adds no companion service. This advances #52, with Phase 3 still open.

```mermaid
sequenceDiagram
  participant Operator as Deployment job
  participant Runner as heimdall-migrate
  participant DB as Application PostgreSQL
  Operator->>Runner: Explicit initialize flag, pinned SQL, secret DSN
  Runner->>Runner: Verify SQL SHA-256 before connection
  Runner->>DB: BEGIN READ COMMITTED
  Runner->>DB: Acquire exclusive advisory transaction lock
  Runner->>DB: Require writable primary; inspect namespace
  alt Namespace absent
    Runner->>DB: Create schema, tables and revision trigger
    Runner->>DB: Insert checksum ledger and ready metadata
  else Namespace present
    Runner->>DB: Verify exact supported metadata and ledger
    DB-->>Runner: Compatible or fail closed
  end
  Runner->>DB: COMMIT
  DB-->>Runner: Confirmed result
  Runner-->>Operator: Ready or classified failure
```

From `sdk/go`, run `go run ./cmd/heimdall-migrate -initialize-session-schema -sql ../../contracts/persistence/session/0001.sql`, with `HEIMDALL_MIGRATION_DATABASE_URL` supplied by the deployment secret environment. The target is a dedicated application database, never ZITADEL or OpenFGA's internal database. A packaged deployment uses the built command plus the same checksum-pinned SQL artifact.

Existing valid v1 installation is a no-op. Existing unknown, migrating or incompatible namespaces are rejected without repair. Runtime constructors never invoke this tool. The runner uses lock 74162401 and READ COMMITTED so a waiting runner sees the prior runner's committed installation on its next statement. Schema, ledger and ready metadata commit atomically. Limits are five seconds waiting for locks, twenty seconds per statement, and thirty seconds per operation.

Failures before COMMIT roll back the transaction. A failed COMMIT response is conservatively outcome_unknown. The operator may rerun the exact tool to verify committed state under the lock; no force-adopt or force-repair option exists. The lost-COMMIT-response branch is now covered by the wire-protocol fault test below.

## Evidence

Disposable pinned PostgreSQL 17.11 run `heimdall-records-8cdee906fda145d89b068f3ebbf59e16` verified:

- A non-superuser migration owner lacking PL/pgSQL usage failed during function creation. No partial schema remained.
- After granting that permission, two independent deployment command processes completed with one valid installation.
- Repeated migration of valid v1 succeeded without changing metadata; migrating state was rejected and preserved.
- Holding the exact advisory lock caused a context deadline; after releasing it, the runner succeeded.
- Untrusted SQL bytes were rejected before connecting.
- Both SDKs' readiness, encrypted record, CAS, cancellation and cross-language reads still passed using the command-installed schema.

The live harness now invokes the deployment command instead of installing schema SQL directly. It removes the container afterward. Evidence is local driver/tool integration; the earlier [[phase-3-persistence-schema|Railway schema proof]] separately validates the same SQL and runtime privilege separation on Railway.

## Compiled runner on Railway

Verification run `839f0c56ca8c480591987cf0bd244eb7` cross-compiled the actual command for Linux, uploaded it with the shared SQL over SSH, and verified the uploaded binary checksum before execution. The binary SHA-256 was `0fba2ef418197b651a23ac684bbfe3ec58af6bb6bb675688e57c02fc5b21db51`.

Two independent invocations installed or verified v1 in a new disposable application database on the existing verification PostgreSQL service. Independent SQL reads confirmed the exact migration checksum, ready metadata and six tables. The runner rejected a deliberately migrating state without changing it; after restoring the test state, an idempotent rerun passed.

The probe used the verification container's existing local Unix-socket administrator access, not a production migration credential recipe. It did not expose a database port or install schema in the identity services' databases. Afterward it dropped only its own temporary database and removed its own upload directory, then independently verified both were absent. `tests/integration/migration-runner-railway.py --execute-verification-migration` reproduces this proof against pinned project/environment/service IDs.

## Confirmed COMMIT response loss

Local run `heimdall-records-39fa1d59afcd4ebdb48f88ed7b1c5d6c` used a disposable second application database and a loopback PostgreSQL wire-protocol proxy. The proxy forwarded the complete migration, observed the server's COMMIT command-complete and ready-for-query frames, withheld every COMMIT response frame, then disconnected the client.

The real command returned outcome_unknown. An independent direct database connection found ready metadata **before** the rerun, proving that this was a committed response loss, not a failed initialization later repaired by retry. A direct rerun succeeded under the deployment lock. Existing record/readiness tests and bidirectional TS/Go reads also passed. The harness removed the entire container, including both databases.

This test covers one committed outcome with a lost response. It does not make every network error distinguishable or prove any managed OpenFGA mutation safe to retry; the migration's transaction lock and checksum ledger are specific to PostgreSQL installation.

## Remaining Phase 3 work

Runtime credential provisioning, complete adapter construction, identity denial and atomic audit/outbox remain open. The tool supports the only current schema version; future migrations require a tested bridge or maintenance procedure, not automatic acceptance of unknown history. It does not drain incompatible runtime processes or verify backup/restore readiness.
