# PostgreSQL shared migration foundation

Session migration 0001 is the first shared physical SQL artifact. Its exact UTF-8/LF
bytes are SHA-256 pinned in catalog.json and checked by both language suites. Never
edit it after this checkpoint; changes require a new numbered migration. The catalog
is deployment input, not a public SDK API. No runtime constructor executes DDL.

Run in a dedicated deployment-owned application database. The fixed heimdall schema
contains scoped product/environment records; it does not contain product business
data. Do not install it in ZITADEL or OpenFGA internal databases. The verified test
uses a fresh disposable application database on the verification cluster.

The deployment runner must acquire its exclusive migration lock and apply SQL,
ledger insertion and compatible metadata in one transaction. catalog.json checksums
cover only SQL bytes, not runner-generated ledger/metadata statements. The initial
protocol ranges are 1–1. Runtime gets read-only metadata access and no schema CREATE,
ledger write or sequence reset permission. The deployment runner below installs v1; runtime credential provisioning and
complete deployment integration remain open. The integration probe demonstrates
the privilege boundaries.

records stores session/login envelope text, key ID, hashed opaque key, expiry in
integer epoch milliseconds, optional identity hash and a database-issued bigint CAS
revision. Revisions are decimal strings across languages. A trigger replaces any
caller-supplied revision on every insert/update using a NO CYCLE sequence. Delete
and recreate cannot reuse a revision; gaps from rolled-back writes are expected.
CAS/expiry predicates belong in every storage update, and are tested by the probe.
Rows are not automatically hidden or deleted at expiry.

The identity hash will use the shared canonical issuer/subject encoding and SHA-256;
no raw principal identifier is used as a potentially oversized B-tree index key.
Identity deny writes and local session invalidation must share a transaction. The
schema alone does not implement reactivation policy or bounded identity freshness.

Audit events have scoped unique IDs, payload digests and recorded timestamps.
Deliveries reference the exact scoped event and cannot be marked delivered without
a timestamp. Runtime is granted INSERT/SELECT only for events; UPDATE/DELETE is
reserved for explicitly authorized retention tooling. Delivery cleanup and retention
must preserve pending, quarantined and held data; no automatic TTL is installed.
Append and initial outbox rows must commit together before returning a receipt.
Idempotent replay must compare the existing digest, never overwrite an event.

SQL checks only storage shape/scope. SDKs must still validate full event schemas,
compute the canonical payload digest, encrypt/decrypt and verify record associated
data, and compare indexed fields against authenticated payloads. Synthetic envelope
strings in database tests are not crypto validation or production session examples.

Managed workspace/operation/index migrations remain separate and are not installed
by the session migration. Both selected compositions require audit persistence.

Verification: python3 tests/integration/persistence-schema-railway.py
--execute-verification-persistence, using the configured Railway CLI and verification
SSH key. Exact project/environment/service IDs are pinned in the probe. It creates
and finally drops only its random application database and NOLOGIN runtime test role.
The probe uses SET ROLE to check privileges, not a production login configuration.

Internal record adapters now share the following v1 payload wrapper inside hr1:
UTF-8 JSON [expiresAtMillis, unpaddedBase64urlPayload]. Maximum caller payload is
700000 bytes. The opaque ASCII key is SHA-256 hashed; its lowercase hex digest is
the RecordCipher recordKey associated-data field, and its raw digest is key_hash.
Expiry from the decrypted wrapper must exactly match the indexed expiry column.
This is a storage encoding, not a validated session schema. Key rotation reads old
key IDs from the configured ring and writes the active key; no background rewrite.

Create reports conflict for an existing scoped key, including an expired row.
Get returns absent for expired/missing records. CAS requires the exact positive
int64 decimal revision and an unexpired row; stale/missing/expired returns absent.
Delete is scoped and idempotent. A record expiring between SQL and decryption also
returns absent; successful persistence never implies a usable authenticated session.
There is no automatic retry. A caller cancellation or five-second deadline before
connection acquisition/dispatch returns cancelled/deadline_exceeded. An interrupted
or otherwise failed dispatched write is conservatively outcome_unknown (except a
known unique constraint rejection). Reads fail closed. TS destroys the checked-out
connection on cancellation; Go uses database/sql context cancellation through pq.
Neither connection close nor cancellation is a proof that a dispatched write rolled
back. Pool acquisition remains subject to the driver's own queue lifecycle.

The caller owns the pool, TLS settings, pool sizing/error monitoring and encryption
key ring. Runtime readiness, credential construction, payload schema validation,
identity denials, audit transactions and higher-level adapter composition remain
required. No public SDK export is added at this checkpoint.

Run python3 tests/integration/postgres-records.py with Docker, pnpm and Go on PATH
for a fresh pinned local database, concurrent CAS, encryption/scope checks,
cancellation and actual bidirectional TS/Go reads. The disposable port binds only
127.0.0.1. Live tests skip without HEIMDALL_TEST_DATABASE_URL; the harness runs both
writer and peer-reader passes. Regular unit tests do not substitute for this proof.

Internal checkReadiness/CheckReadiness helpers read v1 metadata and ledger in one
SQL snapshot, require a writable primary connection and reject unknown history.
The bundled checksum is tested against the actual catalog in both languages.
They perform no DDL and do not fence running processes. The eventual adapter
factory must invoke them before exposing session operations; the current low-level
record constructor remains a composition primitive. Metadata compatibility does
not itself prove table/index completeness or runtime privilege coverage.

Deployment command (run from sdk/go with HEIMDALL_MIGRATION_DATABASE_URL supplied
by the deployment secret environment):

go run ./cmd/heimdall-migrate -initialize-session-schema -sql ../../contracts/persistence/session/0001.sql

This one-shot Go tool is shared deployment infrastructure for both SDK languages,
not an authentication service. Package the built binary and the exact SQL artifact
together. It validates the pinned checksum before connecting, then uses a READ
COMMITTED transaction and advisory transaction lock 74162401. SQL, ledger and ready
metadata commit together. An existing namespace must pass v1 readiness; the runner
never adopts, repairs or upgrades an unknown namespace. An existing valid v1 is a
no-op. A five-second lock timeout, twenty-second statement timeout and thirty-second
operation deadline bound execution. COMMIT response loss is outcome_unknown; rerun
the exact tool to inspect committed metadata under the lock, not to force recovery.
A migration operator must still target the dedicated application database and
coordinate/drain incompatible runtimes. The command does not provision runtime
credentials or claim future-schema rolling-upgrade support.

Audit append is now an internal TS/Go transaction primitive. The factory must
supply schema and semantic validation; the primitive snapshots and validates the
event before dispatch, binds it to its configured product/environment, computes
the shared typed-value digest, and rechecks stored payloads before returning a
receipt. Verified actors and trusted origin are the authenticated caller's job.
The public factory and protected-operation orchestration remain to be composed.

The audit_events.event JSONB column stores exactly six envelope properties:
format="ha1", productId, environment, eventId, recordedAt, and payload. payload is
unpadded base64url of UTF-8 JSON for the complete original event. This preserves
contract-valid U+0000 strings that PostgreSQL JSONB cannot represent directly.
The typed digest covers the decoded original event, not the storage wrapper or
JSON key ordering. Scope/timestamp properties match the columns and the decoded
event. Reject unknown wrapper formats/properties and noncanonical base64url.
JSONB predicates can address routing fields; full event analytics/export must
first decode the payload. The immutable SQL migration and checksums are unchanged.

Append inserts the event and its initial sink delivery rows atomically at READ
COMMITTED with synchronous_commit=on. No UPDATE privilege on events is needed.
An identical scoped event ID/digest returns its original receipt; a different
digest conflicts. Concurrent inserts use ON CONFLICT DO NOTHING, followed by a
new statement snapshot that sees the committed winner. Stored payload corruption
returns audit_unavailable. Receipts are returned only after confirmed COMMIT.
A lost COMMIT response returns outcome_unknown; the caller retains the immutable
event and may replay it to recover the receipt. Errors before COMMIT return no
receipt and roll back. Protected decisions must map persistence failure to the
selected audit_unavailable behavior at the orchestration layer.

Sink IDs are copied, unique, sorted and bounded to 32. A retry preserves the
original delivery set even if current sink configuration differs; retroactive
routing requires separate explicit export/replay behavior. This primitive does
not run deliveries, apply retention, implement backpressure, batch appends or
verify the database's physical fsync/replication guarantees. Those remain required.
