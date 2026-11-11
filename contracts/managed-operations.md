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
Storage does not dispatch remote writes, activate workspaces, authenticate actor
arguments or implement creation eligibility. No retry clears quarantine. Existing
state transitions in tests are administrative fixtures, not implemented methods.

Shared decoder vectors: fixtures/operation-journal.json. Live proof:
`tests/integration/managed-schema.py`, including same-key races, changed-payload
conflicts, TS/Go reciprocal reads and confirmed COMMIT-response loss.
