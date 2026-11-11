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

Current implementation includes the shared classifier, prepared compact identities,
durable holds, metadata quotas, retirement markers, internal identity/receipt reads
and privileged atomic-removal proofs described below. None enables public runtime
retention. Compatible atomic runtime identity writes, explicit retry obligations,
authorized administrative composition, bounded migration/backfill and v4 activation
remain required before retention can be enabled.

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
Whole completed/recovered decisions now have atomic-removal and marker-aware read
proofs. Cross-process tests exercise holds on every member in both commit orders,
observing a real PostgreSQL lock wait before releasing the winning transaction.
All payloads survive a winning hold; a winning retirement removes the whole group
and refuses the late hold. Compact event/decision identities remain unchanged and
the losing transaction adds no metadata charge. This proves the prepared SQL
protocol under READ COMMITTED, not authorized runtime orchestration.
Authorized administrative events, explicit retry-obligation checks, bounded
maintenance and v4 compatibility/admission remain required. The immutable marker is the future
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

## Prepared retained receipt resolution

Internal TS resolveRetainedReceipt and Go ResolveRetainedReceipt now validate and
snapshot a complete retry event, inspect its scoped retained history, and compare
the original digest and recorded time. An identical live or retired event returns
its original receipt fields. A changed event under an existing product/environment
ID returns conflict, including a change of tenant; it must not masquerade as a new
ID merely because tenant-filtered inspection would return no record. A genuinely
unknown ID returns no receipt. Invalid input, readiness/cancellation and integrity
errors retain their existing distinctions.

This resolver performs reads only. It does not append a replacement payload,
recreate deliveries, alter metadata accounting, finalize a decision or authorize
re-execution of business work. Tenant-filtered inspection remains unchanged for
its existing callers; the retry comparison privately resolves the globally scoped
ID before comparing the validated full event. The original payload is never
returned from this receipt path.

Shared retained-retry fixtures and live tests cover completed and recovered
retired members, changed payload/tenant, unknown IDs and invalid input, with
unchanged event/outbox/metadata snapshots after both SDK suites. Public append
integration must call this logic within the compatible atomic write protocol;
this standalone internal resolver does not itself make a subsequent insert safe
against a concurrent writer or publish v4.

## Prepared explicit retry obligations

`0004-retention-retry-holds.sql` extends the existing hold ledger with immutable
purpose: `administrative` (including adopted historical holds) or `retry`. A retry
obligation is a durable payload hold, identified by the same scoped hold ID and
bound to an exact event/tenant. It protects the complete decision when placed on
any member. The two purposes share the existing identity-lock protocol, original
placement/release audit references, immutable history, foreign keys and one fixed
8192-byte metadata slot. No duplicate protection table or extra accounting pool
is introduced. Adoption validates every expanded row against that slot size.

A retry obligation remains active until its explicit release commits. There is no
TTL, lease expiry, clock-based release or inference from a missing caller process.
Independent obligations need distinct hold IDs; releasing one does not release
another. Purpose cannot change, even after release. Exact release retries remain
no-ops and consume no extra quota. Release at full metadata capacity is possible
because the row's existing slot covers the transition; it refunds no history.

Retirement's immediate and deferred guards already examine every active hold,
regardless of purpose. Therefore retry acquisition competes on the same retained
member lock, prevents retirement of the group, and refuses after payload removal.
Future eligibility readers must distinguish active administrative holds from
active retry holds to populate the classifier's held and retry-protected states;
neither state can be inferred from caller flags. Compact identities continue to
protect receipt-only retries after all payload obligations are explicitly released.

This preparation does not determine who may acquire/release obligations or prove
that business/recovery retry obligations have ended. Authorized SDK orchestration
must validate full administrative events and atomically compose the reference,
identity and hold transition. The prepared SQL still checks indexed references
only. Runtime grants, v4 publication and automatic integration remain absent.

## Prepared primary eligibility inspection

Internal TS inspectRetentionEligibility and Go InspectRetentionEligibility now
classify an ordinary event or request-represented decision using one primary SQL
snapshot. Inputs are exact tenant/event identity, explicit current time and a
positive retention duration (TS defaults to the shared 30-day constant; Go accepts
that constant explicitly). Callers cannot supply hold state, retry state or delivery
acknowledgements. Administrative and retry protections come from the durable hold
ledger, across all group members. Released rows do not protect the group.

The reader composes existing marker-aware member inspection with full decoded
history construction. It verifies payload schema/digest, immutable member identity,
charged routing and source decision/reservation agreement before classifying.
Corruption still fails even if the group is held. All members, delivery facts and
protections use the same statement snapshot. At most three group members plus the
representative are materialized, each capped at 1500000 stored-envelope bytes;
delivery lists are capped at 33 so a count above the supported 32 is rejected.
The existing five-second query bound and cancellation behavior apply.

Unknown IDs and other tenants return absent. A terminal or recovery member is not
a valid group representative. Intentional retired payload returns not_found;
missing payload without retirement evidence remains audit_unavailable. Readiness
failure remains unavailable. No payload is returned to the caller and no storage
is mutated. The port requires the prepared schemas including retry-purpose holds;
it does not activate v4 or grant public access.

This is advisory inspection, not a reservation or authority to delete. A future
maintenance transaction must lock and revalidate current history, policy, delivery
and protection facts before acting. A snapshot classified as candidate can become
held immediately after the read. Atomic authorized lifecycle orchestration and
compatible runtime identity writes remain required.

## Prepared atomic retained append

Internal TS appendRetained and Go AppendRetained now compose normal audit writes
with retained identity in one transaction. They validate/snapshot the full event
before I/O, acquire existing primary v3 deployment/capacity admission and initialized retention
capacity, and resolve
its retained event ID inside that transaction before any insertion. Exact live or
retired retries return the original receipt without recreating payload, outbox,
source decision or quota charges. Changed digest/time conflicts, including tenant
changes. Missing compact identity for a live event is an integrity failure; this
writer does not silently backfill old v3 records.

For a new ordinary event, payload, routing, payload charge and compact event
identity commit together. A new request additionally commits its source decision,
payload recovery reservation, retained decision and two reserved metadata slots.
A new request cannot reuse any retained decision ID, including a retired decision.
A normal completion requires an existing retained pending decision and live request
identity. It commits source finalization, payload accounting, terminal identity and
retained resolution together, consuming reserved capacity and releasing the unused
recovery slot. A late identity/quota failure rolls back every earlier mutation.

The shared source validation/copy and capacity finalization routines are reused
inside the transaction; the deployment-only group-copy wrapper retains its original
offline table locks. The new writer uses the existing capacity-policy row for
serialization. All eventual maintenance workflows must acquire the same admission
before identity/hold locks, as required by the retirement transaction contract.
An exact receipt never authorizes fresh business work or changes sink routing.

This internal path is not activated public storage. It requires installed prepared
schemas and compatible callers; catalog/backfill/v4 publication and draining old
writers remain mandatory before production use. New unknown completions and
recovery-administration records cannot be inserted individually through this path;
the retained recovery-pair transaction below handles them together. Already retained records of
those kinds may be resolved as immutable receipt-only retries. Five-second operation
bounds, synchronous commit, cancellation and conservative unknown-COMMIT behavior
are preserved. Reciprocal lost-response tests now cover ordinary, request and normal-completion
appends. Each first writer reports unknown after a confirmed COMMIT response is
dropped; the other SDK resolves the exact original receipt. Payload and metadata
used/reserved counters must change by the expected amounts only once, and the
peer retry must preserve the complete database snapshot even with a changed sink
configuration. The recovery composition has its own proof below.

## Prepared atomic retained recovery

Internal TS recoverRetained and Go RecoverRetained now accept a validated immutable
unknown-completion/recovery-administration pair and explicit cutoff. They acquire
current deployment, payload-capacity and initialized retained-capacity admission
in the same order as retained append. The caller remains responsible for verified
initiator identity and recovery authorization.

For a new pair, the retained decision must still be pending, its live request
identity must match the tenant, and its requested time must meet the cutoff. Both
payloads, original outbox routing, source decision/reservation transition, payload
charges, retained member identities and retained resolution commit together. The
two reserved metadata slots are consumed, allowing recovery at full payload and
metadata quotas. A late retained-resolution failure rolls back all preceding
writes and quota transitions. A normal completion arriving after recovery conflicts.

Existing member identities are inspected before writing. Changed event digests
conflict. Exact pair retries require both members, a recovered decision with those
exact links, matching request identity and the same cutoff eligibility. Live
retries revalidate complete decoded history and the source reservation; retired
retries require validated whole-group retirement evidence. Mixed live/retired or
incomplete pairs fail rather than repairing partial state. Exact retries return
both original receipts without recreating payloads or deliveries. Actor changes
cannot revise an existing recovery administration event.

Both ports snapshot inputs before I/O, use five-second operation bounds and
synchronous commit, and report unknown when COMMIT acknowledgement is lost. The
reciprocal lost-response proof retries the same pair from the other SDK against
the primary; complete payload/source/identity/accounting snapshots must remain
unchanged. This is local database recovery, not permission to replay an uncertain
remote OpenFGA or provider mutation. Public v4 activation, catalog/backfill and
maintenance integration remain outstanding.
