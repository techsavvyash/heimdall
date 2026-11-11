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
pagination, rebuild serialization and generic membership intent creation remain open.
