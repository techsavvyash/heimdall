# Managed operation persistence

Internal TS `OperationRecords` and Go `OperationRecords` provide actor-scoped
primary reads and idempotent local workspace creation. They are not public APIs.
Trusted orchestration must authenticate the actor and authorize creation first.

Construction captures product/environment, store/model/hash, manifest version and
audit routing. Deployment must explicitly migrate and check both schema families.
Reads validate indexed scope, canonical payload and identity hashes, fingerprint,
model pins, state, counters and timestamps. Missing or another actor's operation is
null; malformed state, incompatible pins and non-primary reads are unavailable.

Creation parses the bounded intent, computes the established scoped key digest and
fingerprint, and validates SDK-owned allocation/audit metadata before I/O. The raw
key is never persisted. An existing matching key returns its original operation,
including terminal or uncertain state; a changed fingerprint conflicts. Allocation
IDs supplied on retries do not replace the recorded IDs.

A new creation commits a pending, mutation-blocked workspace, immutable pending
operation (generation 1, dispatch count 0), required membership-intent audit event
and initial sink deliveries in one synchronous transaction. No candidate is added.
No result is published before COMMIT. Unique-key losers roll back all local writes,
then reread the winner and compare the fingerprint. Audit/outbox failure rolls back
the whole creation. A lost COMMIT acknowledgement returns outcome_unknown with the
allocated operation ID; retry reads the committed record if it exists.

Calls have a five-second budget and preserve cancellation/error distinctions.
Storage does not dispatch remote writes, authenticate actor arguments or implement
creation eligibility. No retry clears quarantine.

Shared decoder vectors: fixtures/operation-journal.json. Live proof:
`tests/integration/managed-schema.py`, including same-key races, changed-payload
conflicts, TS/Go reciprocal reads and confirmed COMMIT-response loss.

## Durable transitions

Internal `advance` / `Advance` takes the initiating identity, operation ID, expected
generation, transition name and trusted audit/timing metadata. It reads and validates
the operation, computes the existing contract transition, then locks the workspace
before conditionally updating the operation by generation and state. Every managed
writer must follow this workspace-first order. Missing/foreign operations, stale
generations, invalid state edges and incompatible lifecycle gates conflict.

The transaction increments generation once, increments dispatch count only for
`dispatch`, preserves immutable intent, and updates the workspace timestamp and
revision. Timestamp regressions, early dispatch/retry and counter overflow refuse
progress. The five-second call budget includes lookup, locking and commit.

Dispatch requires a blocked pending creation or blocked active existing workspace.
Other non-create transitions may preserve a suspended workspace; none resurrect a
tombstoned workspace. All nonterminal transitions retain the block. Creation
cancellation tombstones the workspace; cancellation is permitted only before any
dispatch. An uncertain operation has no outgoing transition. There is no force,
lease-expiry or reread-based unblock route.

Completion requires durable `confirmed` state. In the same transaction it inserts
or updates the target candidate for create/add/assign/transfer, or removes that
candidate for remove_member. Upserts preserve the original position, allocate a new
revision and refuse principal mismatch or an older observation. Ownership transfer
does not remove the source actor's membership hint. Creation becomes active; existing
workspace lifecycle is preserved, including suspension. Completion releases the
mutation block only with the operation, candidate and audit/outbox commit.

Each edge has a required audit event. `dispatch` uses membership/ownership attempt;
`retry` uses intent; other edges use completed with succeeded for confirm/complete,
outcome_unknown for uncertain, and failed otherwise. The action is
`<operation_kind>.<transition>` so remote confirmation is distinguishable from local
completion. Ownership uses the ownership event family. Attributes remain empty.
Audit failure rolls back all operation, workspace and candidate changes. A lost
COMMIT acknowledgement returns outcome_unknown with the operation ID; reread state
before deciding what to do. Repeating a stale transition conflicts rather than
incrementing attempts twice.

This is a trusted persistence port, not an evidence verifier. Future remote
orchestration must prove a definitive result and postconditions before `confirm`,
prove no mutation occurred before `rejected`, and authenticate/authorize callers.
It must not send a remote write unless its own dispatch transition returned a
successful commit. If dispatch COMMIT is uncertain, it must not send; a stored
dispatching state stays blocked until conservative recovery establishes safety.
Tests seed confirmed membership records administratively; they do not prove remote
mutation, last-owner policy, delegation, enrollment or operator recovery.

Shared scenarios: `fixtures/operation-transitions.json`. PostgreSQL tests cover
generation races, shared transition sequences, atomic rollback, candidate completion,
cross-language reads and confirmed dispatch COMMIT-response loss. Candidate reads,
pagination orchestration and rebuild serialization remain open.

## Membership intent reservation

Internal `beginMembership` / `BeginMembership` accepts add_member, remove_member,
assign_roles and transfer_owner intents, a scoped idempotency key, trusted allocation
and audit metadata, and the expected workspace revision. The revision is a canonical
positive signed-bigint decimal string. It is a concurrency precondition, not proof
of authorization. The intent and allocation must identify the same workspace.
Creation stays on the separate create entry point; the methods share one validated
persistence implementation without accepting each other's operation kinds.

Before recording new work, the transaction conditionally updates the scoped workspace
at the expected revision, requiring active lifecycle, an unset mutation block and
nonregressing recorded time. The workspace is locked first, its current persisted
fields are validated, and its mutation block is set. Then the immutable pending
operation and required audit/outbox records commit together. Existing candidate
rows are unchanged until confirmed completion. The unfinished-operation uniqueness
constraint remains a second guard against conflicting journal entries.

Same key and fingerprint returns the existing operation before testing the current
workspace revision. This preserves retry behavior after reservation, completion,
cancellation and uncertainty. Changed payload conflicts. Concurrent duplicate
losers roll back before reading the winner; different-key losers conflict. No
retry clears a block, rewrites intent, creates an extra audit event or dispatches a
remote mutation. Ownership uses heimdall.ownership.intent; other kinds use
heimdall.membership.intent. The action is the operation kind.

Required audit failure rolls back both the reservation and intent. An uncertain
COMMIT acknowledgement returns outcome_unknown with the allocated operation ID;
subsequent lookup or same-key retry can recover a committed pending record. The
five-second budget and cancellation rules are shared with creation. A safe
pre-dispatch cancellation preserves active lifecycle and releases the block;
a later new request must use the resulting current revision and a new key.

This port requires trusted orchestration to authenticate the actor, verify current
membership/delegation and product role definitions, protect ownership invariants,
and establish remote results. Revision CAS does not fence OpenFGA requests or
out-of-band changes. Public administration and conservative operator recovery
remain separate requirements; the storage method must not be exposed as an
unauthenticated mutation endpoint.

The shared fixture is fixtures/membership-intents.json. Live tests cover all four
kinds, gates, stale revisions, duplicate and competing races, uncertain reservation,
audit rollback, cancellation, cross-language retries and confirmed COMMIT loss.

## Maintenance scans

Internal `scan` / `Scan` is for trusted product maintenance, not end-user operation
listing. It intentionally reads all initiating identities within the configured
product/environment; every payload still binds to its indexed canonical actor and
target hashes, fingerprint, manifest and model pins. Public operation lookup remains
actor-scoped. A maintenance result is not a newly authenticated principal.

Three fixed queues prevent accidental replay classification:

| Queue      | States                 | Time filter                   |
| ---------- | ---------------------- | ----------------------------- |
| runnable   | pending, retryable     | nextAttemptAt <= now          |
| recovery   | dispatching, uncertain | None; age never proves safety |
| completion | confirmed              | None; local completion only   |

Terminal records are excluded but retained in storage. Unknown queue names,
malformed positions, unsafe timestamps and limits outside 1..100 are invalid.
Ordering is the immutable operation ID under PostgreSQL C collation, with a strict
after-ID keyset. Null starts a traversal. One primary query selects at most limit + 1
records, then bounds the sum of encoded record sizes to 4 MiB before returning them.
JSON array framing is additional bounded overhead. A five-second budget covers I/O.
A short byte-bounded page has continuation even if it contains fewer than limit
items. Continuation always references the final returned ID, never an omitted row.

All returned rows, including count lookahead, pass the same full journal decoder.
Bad bindings/model pins, unexpected states/order and non-primary reads fail the
whole page. Primary status is checked even for an empty page. No work is claimed,
no attempt is incremented and no audit/mutation is performed by selection.
Multiple workers can see identical records; subsequent generation-checked
transitions determine who can proceed. Only a successfully committed dispatch
transition permits that worker to send a remote request.

This is a live traversal, not a durable scheduler checkpoint or snapshot. New work,
state changes and retry deadlines behind the current ID require a new sweep from
null. Row/time/output bounds do not promise bounded total database work. Corrupt
records require inspection; they are not silently filtered or replayed. Recovery
queue membership never authorizes retry, cancellation or unblocking: verified
quiescence and reconciliation remain mandatory. Automatic recovery orchestration,
operator authorization and deployment controls are not implemented by this scan.

## Activated retained runtime composition

TS `OperationRecords.forRetainedRuntime` and Go `NewRetainedOperationRecords`
select the internal audit-v4/managed-v1 composition without constructor I/O or
implicit migrations. The original internal constructors remain legacy schema
ports for historical migration tests; runtime orchestration must select the
retained factory.

Actor-scoped reads, idempotency lookups and maintenance scans evaluate both schema
families' readiness in the same primary SQL snapshot as their result. An absent
record or empty queue does not bypass readiness. Legacy audit versions, migrating
state, incompatible managed metadata/ledger and read-only connections return
unavailable, not a successful absence. Input and cancellation validation still
precede database work.

Creation, membership reservation, transitions and candidate repair all use retained
atomic audit. Shared deployment admission and payload/metadata quota locks precede
managed SQL; managed schema readiness is checked under that admission lock. Managed
writers then retain workspace-before-operation/candidate lock ordering. The event,
outbox, payload charge, compact event identity and metadata charge commit with the
managed mutation or roll back together. Same-key retries may read the original
operation; they never clear uncertainty or replace its allocation. A lost COMMIT
response preserves the existing outcome_unknown and operation-ID behavior.

These factories do not authenticate actors, validate remote confirmations or
implement a scheduler. Standalone workspace/candidate readers now have matching retained factories
(documented in managed-candidates.md). Public managed orchestration remains open. No public
managed endpoint or automatic quarantine recovery is introduced.
