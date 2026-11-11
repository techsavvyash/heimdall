# Audit retention and immutable retry identity

This completes the retention semantics behind the selected 30-day delivered-event
policy. It does not authorize a production purge or claim the persistence adapter
implements deletion yet. Existing pinned migrations remain unchanged.

## Scope and retention units

Retention always selects one exact product, environment and tenant. A null tenant
selects product-level events; it is not a wildcard for all tenants. Authentication
and privileged tenant-scoped authorization precede any retention invocation.

An ordinary event is one unit. Authorization decisions are indivisible groups:
requested only while pending; requested and normal completion when completed;
requested, outcome_unknown completion and recovery administration when recovered.
All member identities, full envelope/digest/context bindings and original routing
must validate before a unit can be considered. A recovery administration record
cannot be purged separately from its decision. Missing or corrupt history is an
error, not a reason to skip validation or discard data.

## Executable eligibility classifier

Internal TS classifyAuditRetention and Go ClassifyAuditRetention accept a validated
metadata snapshot of a group, an exact expected scope, explicit current time and
retention duration. The default duration constant is 2592000000 milliseconds
(30 days). Deployment policy may select another positive safe-integer duration;
changing that policy requires privileged authorization and durable audit.

The classifier validates every member and delivery before returning a retention
reason, even if the group is held. It checks exact group cardinality, scope,
unique event IDs, unique sink IDs (at most 32 per event), safe timestamps, known
states and coherent acknowledgement timestamps. Pending/quarantined routing has
no acknowledgement time; delivered routing requires one. No configured sinks is
an explicit empty list, not missing metadata. Product-level tenant omission and
null normalize to the same scope here; full event digests still distinguish the
original payloads.

Reasons, in deterministic priority order, are retain_held, retain_unresolved,
retain_undelivered, retain_retry, retain_age and candidate. A hold on any member
protects the group. A pending decision protects its original request. Pending or
quarantined delivery to any original sink protects the whole group. An explicit
active retry obligation protects the payload even if other conditions are clear.

Age starts from the maximum recordedAt or deliveredAt across every member. Thus
late SIEM acknowledgement does not immediately erase an old backlog. A clock
regression or future timestamp retains the group rather than shortening its age.
Equality at the retention boundary is eligible; one millisecond earlier is not.
The calculation uses subtraction so adding duration to a near-maximum timestamp
cannot overflow the shared JSON safe-integer range.

A candidate is not purge authorization or evidence that receipt obligations have
been fulfilled. The classifier performs no I/O, DDL, mutation or archival and
cannot replace full history validation. Shared fixture audit-retention.json pins
TS/Go behavior. Hold state and retry state are explicit validated storage facts,
not untrusted request flags or lease-expiry guesses.

## Preserve exact retry semantics after payload retirement

The existing contract promises that identical appends return their original
receipt and changed payloads under the same scoped event ID conflict. Payload
retention must not silently turn a previously used ID into a new event. Before
removing payloads, retain immutable compact identities containing the scoped event
ID, recordedAt, portable payload digest and original routing/binding metadata.
Resolved decision identity and its request/terminal/administration links must also
survive. A new request must not reuse a retired decision ID.

Exact retries validate the supplied full event and compare these immutable
identities before returning the original receipt. Changed values conflict. Such
receipts never authorize reevaluating an old decision or replaying business work.
A retained identity is not an exportable event: it cannot fabricate a missing
payload or create new SIEM deliveries. Payload-dependent historical reads return not_found when retained identity exists
and an immutable retirement marker proves its payload was intentionally removed.
Missing payload without that marker is audit_unavailable, not successful retention.
Readers cannot pretend that
a retired event never existed or reconstruct an authorization result from hashes.

This deliberately preserves retry identity without retaining the full personal
payload forever. Compact identities still consume storage. They require explicit
count/byte budgets and accounting, including terminal/recovery reservations;
retirement must move charges rather than pretend that all bytes were freed.
Capacity exhaustion blocks new work instead of dropping identity metadata. No
unbounded, unmetered receipt cache or automatic ID recycling is acceptable.
Supported identity archival/removal would require a separately reviewed retry
contract; it is not part of this default policy.

## Required persistence transaction and deployment gate

The future retirement transaction must acquire current runtime/deployment and
capacity admission, lock the complete group and its hold/routing/retry facts in a
consistent order, revalidate history and current policy, install/verify compact
identities and an immutable retirement marker bound to the administrative audit,
remove payload/outbox/slot material in valid reference order, transfer
capacity accounting and commit its required administrative audit together. A new
hold, delivery retry or competing finalizer cannot race around those checks.
Failure rolls back the entire group. Unknown COMMIT requires inspecting immutable
retirement identity; it does not authorize a blind second deletion or refund.

The runtime role receives no general DELETE grant. A bounded, privileged
maintenance port must enforce scope, count, bytes, cancellation and audit. The
policy clock is explicit and trusted; there are no implicit cleanup timers.
Migrations must install and backfill the required identity/hold/accounting state,
validate existing history and require compatible writers before retirement is
enabled. Existing v3 capacity alone is insufficient for this gate.

Current implementation includes the shared classifier and the prepared compact
identity schema below. Durable holds, runtime identity integration, additional
accounting, atomic retirement, migration/backfill,
concurrent-hold/retry fault tests and public authorized maintenance remain required
before retention can be enabled. No current event deletion is added by this work.

## Prepared compact identity storage

`persistence/session/0004-retention-identities.sql` now prepares event and decision
identity tables in the private heimdall_retention namespace. It does not register
runtime version 4, change readiness metadata, copy history or enable deletion.
The shared audit-retention-identities.json fixture pins its checksum. Existing
migration ledger entries and SQL remain unchanged.

Event identities preserve scope, tenant, event ID, recordedAt, digest, original
sorted sink routing and membership kind/decision ID. Inserts must match an existing
capacity-charged event's indexed time/digest, its actual sink rows and charged
delivery count, and its unique
request/terminal/recovery binding. Ambiguous bindings refuse. Every identity
field is immutable and deletion is refused; exact no-op updates are permitted.
No original event payload, actor or attribute body is copied into these rows.

Decision identities reference these persistent event identities, not deletable
payload rows. Inserted request/time/resolution/terminal/administration values must
match the existing audit slot and capacity reservation. Each referenced member
must agree on decision, tenant, role and indexed time. Pending identities can
advance only to a matching observed source resolution. Their request binding
never changes, and a resolved identity never changes again. Foreign keys reject
missing members. Both schemas keep all identifiers product/environment scoped.

These SQL checks validate indexed structure, not full ha1 payload semantics or
cryptographic correctness. The future SDK/backfill port must decode the full
original event, validate its digest/schema and derive tenant/group metadata before
inserting identity. SQL deliberately does not parse the encoded payload, which
can contain values PostgreSQL jsonb cannot represent directly. This file alone
also does not serialize concurrent v3 writers with retention or prevent old
writers from recreating payloads. Drained-writer migration, v4 admission and
coordinated append/finalization are required before activation.

Pinned PostgreSQL proof covers transactional DDL rollback, copy of live TS/Go
history, source mismatch and immutable-field rejection, FK completeness, a real
SDK finalization followed by a matching compact transition, and identity survival
after privileged test-only removal of an ordinary payload and a completed group.
The test deletion bypasses the unimplemented hold/age/authorization workflow; it
is not a production purge procedure. Runtime roles receive no namespace access.
Metadata accounting/reservations, holds, retirement markers, SDK identity writes/reads, complete
backfill/activation and authorized retirement remain outstanding.

## Decoded history identity construction

Internal TS buildRetentionIdentities and Go BuildRetentionIdentities now validate
one complete decoded built-in event group before producing compact identity rows.
Inputs contain the expected exact tenant scope, resolution, each full event and
its stored digest, original sink IDs and charged delivery count. The storage
caller must first validate/decode the ha1 envelope and load these facts from a
consistent primary snapshot; caller-supplied routing is not evidence by itself.

The builders validate full event shape and semantics, recompute portable digests,
require exact product/environment/tenant binding, reject duplicate event/sink IDs,
and require routing cardinality to equal its original charge. Missing routing is
invalid; an explicit empty list represents no configured sinks. They derive tenant,
kind and decision membership from validated events, correlate request/completion
contexts, and validate recovery administration's actor, target, timestamps and
deterministic event ID. Missing or orphaned members and a resolution inconsistent
with history fail as audit_unavailable. Invalid configured scope is invalid_argument.

Output is sorted by event ID, with sorted copied routing and no payload/actor/body.
Ordinary events have no retained decision link, even if the event carries a
correlation decisionId. Authorization recovery administration cannot be treated
as an ordinary event. No source objects are mutated or shared mutable routing
returned. This does not persist rows, acquire locks, authorize deletion or verify
holds. These builders accept built-in events only; custom event registry integration
remains part of the mandatory enterprise/custom-audit work before release.

## Prepared PostgreSQL group persistence

Internal PostgreSQL RetainIdentityGroup/retainIdentityGroup now reads and copies
one explicit group of at most three event IDs. This deployment-only port requires
drained writers, the pinned prepared identity schema and existing v3 readiness.
It does not publish v4, grant runtime identity access or enable retirement.

The transaction takes the exclusive deployment advisory lock, current capacity
admission, and SHARE locks on the source event/delivery/decision/charge/reservation
tables. Reads then validate each bounded ha1 envelope, indexed identity/time,
digest, exact envelope byte charge and original charged routing. The complete
semantic builder verifies the requested resolution and exact tenant scope before
any insert. Event identities and the linked decision identity commit together;
SQL triggers compare source bindings and refuse changes to immutable identities.
An exact retry compares/upserts identical rows; a matching pending identity may
advance to the current source resolution. A late failure rolls back all inserts.

Both ports use a five-second operation deadline and synchronous commit. Cancellation
before dispatch returns cancellation; a lost COMMIT response remains outcome_unknown.
No returned identity authorizes business work. Readiness, catalog registration,
metadata quota/reservations, checkpointed backfill, publication and runtime
integration remain required before this prepared port can become an operational
retention workflow. Its table locks are intentionally an offline mechanism, not
an acceptable online cleanup strategy. Payloads remain untouched.

## Prepared durable hold ledger

The separate `0004-retention-holds.sql` prepares an append-preserving ledger of
scoped hold IDs, target retained event IDs, placement time/reference and optional
release time/reference. It does not register v4 or grant runtime access. Multiple
hold IDs may protect the same event independently. Any active hold on a decision
member protects the complete group; releasing one hold never releases another.

Placement fields cannot change, rows cannot be deleted, and a released hold cannot
be reopened or revised. Exact no-op updates are allowed. Release requires a
distinct audit reference and a timestamp no earlier than placement. New holds must
start active. Target and audit references are product/environment scoped and
must have the same tenant, including exact null semantics. Target payload must
still exist; the audit reference must have live payload whose indexed time and
digest match its retained identity. Full ha1 validation and administrative event
purpose/actor/authorization are deliberately not attested by these SQL checks.

The trigger locks the target retained identity. Future retirement must lock all
group identities in deterministic order and then check active holds before
removing any payload. This shared lock protocol has not yet been wired to a purge
port, so the ledger alone does not prevent privileged direct payload deletion.
The SDK hold workflow must atomically persist its authorized administrative event,
compact reference and hold transition with metadata accounting. Full history
validation, hold quotas, retirement exclusion, v4 admission and public APIs remain
required. Tests of this prepared schema use existing events as reference stand-ins;
they prove structural constraints, not authorized administrative actions.

## Prepared metadata capacity

`0004-retention-capacity.sql` adds an explicit deployment-wide logical quota for
retained event identities, decision identities and hold history. Configuration
requires both row and byte limits; writes refuse while uninitialized. Adoption
recounts all existing rows and pending obligations instead of trusting supplied
usage counters. Installation/adoption requires drained writers and an enclosing
migration transaction; runtime activation is still absent.

Each row consumes a conservative 8192-byte logical slot. SQL rejects a serialized
metadata row larger than its slot. Resolution and release updates keep the slot,
so hold release requires no extra quota and never refunds immutable history. This
trades storage utilization for a simple bound on future metadata growth. It is a
logical admission budget, not a measurement of physical PostgreSQL pages, indexes,
WAL or disk space; physical capacity monitoring remains necessary.

A pending decision reserves two additional slots for its terminal and recovery
administration identities. Inserting either member consumes one reservation;
resolving the decision releases any remaining unused reservation. Already-resolved
history reserves nothing. All used/reserved changes serialize on one capacity
row and participate in the identity/hold transaction. A failed quota check rolls
back inserted metadata, and exact retries or release updates do not double-charge.

The prepared live proof covers initialization over existing mixed-language
history, uninitialized/insufficient quota refusal, competing holds for the final
slot, release at full capacity, recovery consuming reserved slots at full capacity,
and accounting-stable retries. It does not yet prove online v4 request admission,
atomic administrative hold creation, or the complete bounded backfill/activation
workflow. Future runtime transactions must create the request identity, pending
decision and reservation together before releasing protected work. No grants or
compatibility version are changed by this SQL.

## Prepared retirement marker integrity

`0004-retirement-markers.sql` adds permanent scoped retirement IDs, exact sorted
member IDs, tenant, recorded retirement time, selected retention duration and an
administrative audit reference. The representative must be an ordinary event or
the request of a resolved complete decision. Markers and their membership cannot
be rewritten or deleted. Each marker consumes one metadata slot; re-adoption
includes these permanent records in quota usage.

Before insertion, SQL locks retained member identities in stable order and current
delivery rows, rejects active holds and validates indexed event/digest/charge/
routing agreement. Every event and delivered acknowledgement must satisfy the
recorded duration; pending/quarantined or missing delivery history refuses.
The administrative reference must be separate, tenant-bound, present and recorded
at retirement time. These are indexed checks, not full administrative event or
actor authorization. Full SDK envelope/semantic validation remains mandatory.

A deferred constraint checks at commit that all listed payloads are absent, no
member has an active hold, and the administrative payload remains present. Thus a
marker-only transaction cannot claim completed retirement, and a hold inserted
later in the same transaction cannot be bypassed. Failure rolls back marker and
metadata accounting with the attempted transaction. Existing compact identities
remain. Exact no-op marker updates do not recharge or require retired payloads.

This is prepared SQL, not an enabled purge API. The live positive test retires an
ordinary event in a privileged disposable database with a reference stand-in.
Authorized administrative events, explicit retry-obligation checks, whole-decision
and cross-process retirement race proofs, marker-aware readers, bounded maintenance,
and v4 compatibility/admission remain required. The immutable marker is the future
reader's evidence for intentional removal; missing payload without one remains
an integrity error. Privileged direct SQL deletion is not made safe by this schema.

## Prepared marker-aware inspection

Internal TS inspectRetainedEvent and Go InspectRetainedEvent now inspect one scoped
event from a single primary PostgreSQL statement, gated by existing v3 readiness.
The prepared identity and retirement schemas must already exist. This does not
publish v4 or change public audit reads/writes. Inputs require an exact tenant
(including null for product-level history) and event ID. A different tenant or
unknown ID returns no record. A live payload without its expected compact identity,
or an identity whose payload disappeared without a matching marker, fails with
audit_unavailable rather than being silently treated as absent.

A live result includes the decoded, schema/digest-validated original event and
compact identity. Indexed time/digest, charged bytes and original routing must
agree; its retained member role must match the event and retained decision links.
A retired result includes immutable identity metadata and no event payload. It
requires exactly one marker, matching tenant, representative, complete sorted
member list, resolved decision (where applicable), age and administrative identity,
and absence of every listed payload. A marker plus a still-live payload is an
integrity failure. Inspection never reconstructs an authorization result or
creates export deliveries from retained hashes.

Both ports preserve five-second operation bounds and cancellation. This is an
internal diagnostic/read foundation; an authorized payload-only public reader
will translate intentional retirement into not_found. Public receipt retry
integration, full decision/recovery inspection qualification, complete v4 catalog
admission and runtime orchestration remain outstanding.
