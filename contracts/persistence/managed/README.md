# Managed PostgreSQL physical foundation

`managed/0001.sql` is independently checksum-pinned in the shared catalog. Do not
edit its published bytes; later changes require numbered migrations. It installs
`heimdall_managed`, separately from the immutable session/audit `heimdall` schema.
A managed composition requires both in the same application database so workspace,
operation, candidate and audit/outbox writes can share a transaction. Session-only
installation does not create managed tables. No service internal database is used.

The deployment CLI now installs this family explicitly after verifying session v1.
Both SDKs expose internal combined readiness checks, observing both families in one
statement snapshot. Internal workspace readers now validate scoped primary rows in both SDKs. Managed
write/CAS and operation/candidate adapters remain pending; there is no managed
public factory yet.

From `sdk/go`, with `HEIMDALL_MIGRATION_DATABASE_URL` set to the selected application
database by deployment configuration:

```sh
go run ./cmd/heimdall-migrate -initialize-session-schema -sql ../../contracts/persistence/session/0001.sql
go run ./cmd/heimdall-migrate -initialize-managed-schema -sql ../../contracts/persistence/managed/0001.sql
go run ./cmd/heimdall-migrate -status session
go run ./cmd/heimdall-migrate -status managed
```

Choose exactly one mode. Status rejects SQL paths and initialization flags, reads
metadata only, and returns ready or a typed failure. It checks compatibility for a
writer on the primary, so a read-only transaction/replica is not ready even though
the status operation itself does not write. A SELECT-only role can run status.

Managed installation validates exact SQL bytes before I/O, takes the existing
exclusive deployment lock, requires session/audit readiness, and atomically applies
managed DDL/ledger/metadata with synchronous_commit=on. It does not install a missing
base, adopt an incomplete schema, repair metadata, or upgrade unknown versions.
Existing compatible installations are verified without reapplying SQL. Session-only
readiness does not depend on managed readiness. Runtime checks never migrate.

Both families share the database-scoped advisory lock so cooperating installers
serialize. The transaction uses READ COMMITTED deliberately: a waiting installer
sees its predecessor's commit. The overall deadline is 30 seconds, lock timeout
5 seconds and statement timeout 20 seconds; caller cancellation may end earlier.
A lost COMMIT response returns outcome_unknown. An exact rerun rechecks the ledger
under the lock; it does not infer rollback from a disconnected client.

Combined runtime readiness has a five-second deadline and rejects missing or
incompatible metadata/ledgers, unknown checksums, recovery replicas and read-only
transactions. It is a snapshot check, not a fence against later migrations or an
exhaustive table/permission audit. Operators still coordinate/drain incompatible
running versions and must not mutate metadata outside the deployment protocol.

## Physical representation

- Independent schema_metadata and migrations use the same protocol-range/checksum
  shape as session storage. Initial managed protocol/database versions are 1.
- Workspaces use product/environment/workspace ID as their primary key, validated
  display-name bounds, lifecycle, mutation-block flag and millisecond timestamps.
  A noncycling bigint sequence assigns every insert/update a new revision. Identity
  and creation time cannot be updated. Runtime cannot delete workspace tombstones.
- Operations reference the exact scoped workspace. IDs, kind, actor/target hashes,
  idempotency digest, fingerprint, intent bytes, pinned store/model/hash, manifest
  version and creation time are immutable. Canonical identity SHA-256, intent
  fingerprint and idempotency digest come from existing shared SDK contracts.
  `intent_payload` contains UTF-8 JSON bytes, not JSONB, preserving escaped U+0000.
  It may not contain credentials or unbounded product request bodies.
- Operations retain their idempotency uniqueness after completion/cancellation.
  The scoped key digest includes kind and initiating principal under the canonical
  digest contract. A partial unique index permits only one nonterminal operation
  per workspace, including uncertain operations. There is no expiry of that slot.
  `next_attempt_at` supports bounded scheduling of pending/retryable work only.
- Operation generation and dispatch counts are safe integers on the wire. SQL
  checks bounds and disallows cancelled records with a prior dispatch. SDK CAS and
  the shared transition function must enforce the full state machine. SQL alone
  does not prove recovery evidence or prevent an operator from rewriting state.
- Membership candidates use scoped identity hash plus workspace as their key.
  Canonical principal text is stored outside the index and must match its hash.
  A database-issued stable position supports scanning; updates preserve position
  and assign a new revision. Deletion/reinsertion receives a new position. Revision
  and observed-at fields support later reconciliation coordination, but are not a
  membership authorization decision or a snapshot-pagination guarantee.

All revisions/positions are bigint decimal strings for TS/Go adapter APIs, never JS
floating-point values. Runtime gets sequence USAGE, not UPDATE/reset privileges.
Foreign keys include product/environment and use RESTRICT, never business-table
cascades. Operational history has no automatic TTL or runtime DELETE grant.

## Responsibilities above SQL

Adapters must validate complete schemas and semantic contracts, authenticate actor
provenance, recompute indexed bindings/digests, enforce generation CAS, and write
mandatory audit atomically at the selected boundary. They must lock/serialize
workspace lifecycle changes with candidate/operation updates. Nonactive or blocked
workspaces cannot authorize. Candidate rows never independently authorize access.

No timer, lease or successful remote reread can clear uncertain dispatch. Future
recovery must prove quiescence and reconcile before any local unblock. This schema
has no force-unblock function and no automatic retry trigger. It does not itself
supply a recovery protocol or a safe public state-transition API.

## Evidence

`contracts/fixtures/managed-schema.json` drives SQL constraint tests in TypeScript,
Go and the Railway probe. The payload/audit rows in this physical-layer fixture are
synthetic byte/shape examples, not fully validated SDK operation/event envelopes.
Both language suites pin the actual SQL checksum through compatibility policy.

Run `python3 tests/integration/managed-schema.py` under the repository toolchain for
local PostgreSQL 17.11 tests, including bidirectional driver reads. The optional
`--execute-verification-managed` mode uses the fixed Railway verification project,
a random disposable application database and a NOLOGIN test role. Both modes clean
up owned resources before reporting success. Railway executes the shared SQL cases;
it does not run native SDK processes inside the database service.

## Workspace read boundary

Internal TS `WorkspaceRecords.get` and Go `WorkspaceRecords.Get` capture product
and environment at construction and accept only the workspace ID per read. They
own no connection pool, perform no startup I/O or DDL, and use one primary-query
snapshot with a five-second deadline (or earlier caller cancellation). A SELECT-only
role needs schema USAGE plus SELECT on managed workspaces; compatibility readiness
separately needs the metadata/ledger grants described above.

Missing rows return null/nil only after a successful primary read. Read-only
transactions/replicas, query failures and malformed stored rows return unavailable;
cancellation keeps its own classification. Decoding checks scope, lifecycle,
boolean flags, name Unicode/codepoint/byte bounds, canonical positive bigint
revision, safe integer timestamps and creation/update ordering. Revisions remain
strings. No cache can hide a later committed mutation-block flag. This snapshot
does not fence an already authorized request against a later suspension.

The reader returns a validated record for the existing workspace lifecycle gate.
It is not an authorization API: active/unblocked only permits subsequent identity,
SSO and current OpenFGA membership checks, with mandatory decision audit before
protected work. No mutation or unblocking path is exposed by this read port.
`contracts/fixtures/managed-workspace-read.json` defines shared decode/gate cases;
the managed integration harness tests both SDKs with real PostgreSQL and a
SELECT-only role, including blocked-read deadlines and cross-language row reads.

## Atomic audit composition

The internal audit implementation now owns a transaction that can include trusted
SDK local writes: TS `PostgresAudit.appendWith` and Go `Audit.appendWith` (unexported
in Go). Standalone audit append delegates to this path without extra writes. It
snapshots/validates the event before opening the transaction or invoking mutation
code, then executes local writes, event insertion/idempotency validation and initial
sink rows on one connection with synchronous commit. A receipt is returned only
after the transaction commits. Callback, event or outbox failure rolls back the
whole transaction. A lost COMMIT response returns outcome_unknown without a
receipt even if the database committed all rows.

This is SDK-internal composition, not a callback API for products. Callbacks must
use only local SQL on the supplied transaction/context; they must not commit,
perform remote I/O, ignore cancellation, or publish results. Higher-level managed
methods still own scope validation, actor provenance, intent binding, lifecycle
locks, generation CAS and audit-event meaning. An existing matching audit event
alone does not deduplicate the callback: operation/key checks must do that inside
the same transaction. No arbitrary mutation replay is authorized by receipt lookup.

The shared `managed-audit-transaction.json` fixture proves physical atomicity across
workspace, operation, candidate, audit event and two outbox rows. Its operation
payload remains a synthetic physical example, not a validated onboarding command.
Both native SDK tests exercise rollback, cancellation, immutable event snapshots
and confirmed commit-response loss. Managed mutation APIs remain pending.
