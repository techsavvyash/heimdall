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
ledger write or sequence reset permission. The production runner and credentials
are still being implemented; the integration probe demonstrates those boundaries.

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
