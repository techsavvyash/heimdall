# Audit retention and immutable retry identity

This defines the retention semantics behind the selected 30-day delivered-event
policy. The privileged retirement adapter implements the atomic removal transaction.
Production scheduling and authenticated scope authorization are separate gates.

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

The retirement transaction must acquire current runtime/deployment and
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

The internal TS `retire` and Go `Retire` ports accept a pre-authorized exact
scope, immutable retirement ID, target event ID, policy version, trusted time,
retention duration and full administrative event. The administrative event uses
`heimdall.policy.completed` and the action `audit.retention.retire`. Its ID is
`retirement-` plus the SHA-256 hex digest of the domain
`heimdall.audit-retirement.v1`, product, environment, retirement ID and decimal
retention duration joined by NUL. The scope, target, policy version, operation ID,
actor and time must agree with the request.

The transaction classifies one group inside the audit transaction. The marker
trigger locks the group and its deliveries and rechecks eligibility before
removal. It retains the compact identities, writes the new audit event and
marker, and deletes only the group's delivery, charge, decision and payload
rows. A protected group returns conflict. An exact retry returns the original
audit receipt only after checking the immutable marker and retired target.
Changed event content or marker fields conflict.

A dedicated maintenance connection needs the retained audit grants plus INSERT on
retirement markers, column-level UPDATE on delivery state for the marker's row
locks, and DELETE on the payload, delivery, charge, decision and decision
reservation tables. Its privileges are intended for trusted operator maintenance.
The new audit event and marker require headroom before payload reclamation. The
per-hold release reservation serves hold release only. Production operation also
requires actor authentication, scoped authorization and configured policy lookup.

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

## Offline retention upgrade preparation

The shared Go deployment CLI now accepts --prepare-retention-upgrade with an
explicit drained-writer assertion, six checksum-pinned SQL files and positive
metadata row/byte limits. Both TS and Go applications use this tooling. It requires
an exact activated v3 catalog and readiness receipts before any installation.
Under the exclusive deployment lock and source SHARE locks, it preflights one
slot per existing event and decision plus two per pending decision, at 8192 bytes
per slot. Either insufficient limit refuses without changing readiness.

All prepared retention DDL, quota initialization, six-checksum/limit receipt,
backfill checkpoint and state=migrating transition commit atomically. The schema
version remains 3; no version 4 ledger entry or runtime grants are installed. Old
TS/Go admission refuses while migrating. Exact retries validate the full expanded
catalog and same limits and preserve state; different limits conflict. Partial or
altered schemas refuse rather than being adopted. Lost COMMIT returns unknown and
is resolved by retrying preparation against the primary. The 30-second operation,
five-second lock and 20-second statement bounds preserve cancellation.

The initial checkpoint is ordinary history with no cursor and zero scanned counts;
future bounded backfill must copy complete validated groups and charge metadata,
then prove full coverage before activation. Preparation is not semantic validation
of every historical payload, automatic rollback to v3, or a usable serving state.
Writers and maintenance jobs must remain externally drained until a compatible
activation is implemented and verified. This step does not enable retention.

## Bounded offline retention backfill

The shared deployment command now accepts --backfill-retention-upgrade with drained
writers and a 1–100 group page limit. It requires the exact prepared catalog and
migrating v3 state. Ordinary events are copied first, then complete pending,
completed or recovered decisions. Tenant scope is derived from a fully validated
representative payload; every member is decoded and semantically correlated by
the existing identity builder. Missing/corrupt history refuses the current page.

Pages cap encoded payload reads at 8 MiB, counting the representative's extra read,
and never split a decision. Individual envelopes remain at most 1500000 bytes and
routing at most 32 original sinks. Metadata candidates are count-bounded before
payload loading. A ten-second operation context and five-second lock/statement
bounds limit database work; this is not a claim about physical scan or heap cost.

Compact identities, metadata charges/reservations and the scoped durable cursor
commit together under exclusive deployment admission. Checkpoint counts must agree
with retained rows and accounting before resume. A lost COMMIT returns unknown;
retry resumes persisted progress without duplicate charging. Quota or late member
failure rolls back the entire page. Completion validates source/retained coverage
and accounting, retains state=migrating and publishes no new runtime protocol.
Activation still requires independent full-history validation and compatible TS/Go
public admission/writes. No deletion or retention maintenance is enabled here.

## Atomic retention v4 activation

The shared activation command requires drained writers, an exact prepared catalog,
completed backfill and source/retained quota coverage. Within one locked primary
transaction it independently decodes all current history in bounded group pages
and compares existing compact event and decision identities. This validation mode
is read-only: missing/changed identities or corrupt payloads fail without repair,
quota consumption or checkpoint changes. Thirty-second operation, five-second lock
and 20-second statement bounds apply. The primary/read-write check precedes BEGIN.

A successful transaction adds migration version 4 using the pinned identity-schema
checksum and publishes exactly version-4 readers/writers. The preparation receipt
binds the other pinned SQL files and explicit quota limits. Lost COMMIT reports
unknown; a retry verifies the v4 protocol receipt without reusing historical scan
counts as live counters. Old v3 admission refuses the activated database.

TS and Go now have internal single-snapshot standalone and managed v4 readiness
checks with shared mutation fixtures. These are compatibility checks, not complete
catalog/history validation. Public bearer storage now uses v4 admission and retained
audit writes; trusted internal recovery maintenance also uses retained history.
Managed composition and operational qualification remain incomplete. Activation
alone does not enable retention or satisfy Phase 3 acceptance.

## Internal retained runtime admission

Both SDKs expose an internal restricted retained runtime port for append, recovery,
inspection, receipt resolution and advisory eligibility. It cannot call legacy
append primitives. Writes acquire shared deployment admission, require the v4
protocol receipt, then lock payload capacity and metadata capacity in that order.
Exact retries are subject to the same readiness check. Read operations preserve
one primary snapshot with the v4 receipt predicate and existing scope validation.
Go checks the connection's read-write state before an explicit read-write BEGIN;
read-only and legacy-v3 connections refuse in both SDKs.

The retained-runtime fixture and pinned PostgreSQL harness cover normal/decision
writes, recovery, changed-tenant conflict, migrating refusal, restricted grants,
read-only admission and reciprocal confirmed-COMMIT loss. Activation retry after
new v4 history must preserve all data and accounting. Public factories now compose
retained writes with v4 identity admission. Internal bounded recovery maintenance
also uses these ports and checks readiness even for empty scans. Managed composition
remains unfinished. No deletion is exposed and operational qualification remains
required before production use.

## Local managed transaction composition

The restricted retained runtime port now provides an SDK-internal `appendWith`
boundary in both languages. It snapshots and validates the event before database
I/O, takes v4 deployment admission and both capacity locks, then runs trusted
local SQL and retained append in one transaction. Callback failure, cancellation,
audit conflict, outbox failure or retained metadata failure aborts all local
writes and accounting. A lost COMMIT response returns `outcome_unknown` without
a receipt; observation/retry uses the original retained event identity.

The callback cannot perform remote I/O, commit independently or publish results.
It must enforce its own operation-key/generation idempotency, including when an
exact live or retired event identity already exists. Audit receipt reuse is not
authority to replay a managed operation. This is an internal composition primitive;
managed operation factories still require v4 admission/read integration, and no
public callback extension or retention deletion API is introduced.

## Hold administration binding

The internal TS `validateAuditHold` and Go `ValidateAuditHold` validate a hold
request and its complete administrative event before persistence. This is a pure
binding check, not authentication, scope authorization, an active policy lookup,
or evidence that a retry obligation has ended.

The request fixes product/environment, exact nullable tenant, hold ID, protected
event ID, purpose (`administrative` or `retry`), operation (`acquire` or `release`),
safe-integer timestamp and policy version. A null tenant selects only product-level
history. The caller must obtain time, policy and actor from trusted orchestration.

The audit is a reserved version-1 `heimdall.policy.completed` event with SDK
source, succeeded outcome, a known system/user actor, empty attributes and no
decision ID. Its target is `audit_event` with the protected event ID, its operation
ID is the hold ID, and its action is `audit.hold.<purpose>.<operation>`. Scope,
policy version, occurredAt and recordedAt must exactly match the request. It may
not target itself. Full event shape and existing security-event semantics also
apply; unrelated events cannot serve as the administration reference.

The administration event ID is `hold-` followed by lowercase SHA-256 hex of UTF-8
`heimdall.audit-hold.v1` + NUL + product + NUL + environment + NUL + hold ID + NUL +
operation. Acquisition and release therefore have separate fixed identities.
Changing tenant, target, purpose, policy, actor or timestamp cannot generate a new
receipt for the same scoped hold operation: the eventual transactional writer
must reject changed payloads under that fixed ID. Retries must reuse their
original administrative event, not rebuild one with a new clock value.

Shared `audit-hold-binding.json` tests both purposes/operations, nullable scope,
user/system actors and mismatched or malformed administration. The persistence
implementation must still lock/validate complete target history, preserve hold
immutability, commit audit/identity/hold/accounting together, and handle lost
COMMIT without publishing false success. These validators do not enable holds or
retirement by themselves.

## Internal atomic hold persistence

The retained runtime ports now expose internal TS `applyHold(request, administration,
signal?)` and Go `ApplyHold(ctx, request, administration)`. They require v4 admission
and the full binding above. The caller must authenticate/authorize the actor and
exact scope and supply trusted policy/time. Release authority includes establishing
that a retry obligation has ended; database state alone cannot establish that fact.

The transaction takes the deployment gate, payload-capacity lock and metadata-capacity
lock before hold state and sorted member identity locks. New transitions validate
the live target and every member of its decision, including source decision and
reservation bindings, full events, retained identities and original routing. Missing
or retired target payload returns not_found; corrupt member history refuses. Each
member read is capped at 1.5 MB and a decision has at most three members.

Administrative audit, retained identity, outbox and accounting are written before
the hold mutation in the same transaction. A late hold constraint failure rolls back
every write. The maintenance role adds INSERT/UPDATE on the hold ledger to retained
runtime privileges; it receives no general DELETE or DDL grant. Five-second bounds
and cancellation apply. A failed COMMIT reports outcome_unknown. Independent exact
retry observes the immutable hold and administrative receipt; changed payloads
conflict. An acquire retry after release returns its original receipt and never
reactivates the hold. Missing/inconsistent hold-versus-audit state is not repaired.

The hold row consumes one metadata slot. Release keeps that slot and records an
administrative event. On v4, the administrative event uses the ordinary
payload, outbox and retained-identity budgets and the transaction refuses when
they are exhausted. On v5, the transaction uses the hold's release reservation
described below.

Shared `audit-hold-storage.json` and the activated profile in
`tests/integration/audit-capacity-backfill.py` cover atomic transitions, immutable
retries, rollback, cross-language receipts, concurrent calls, lost COMMIT responses,
whole-decision protection/corruption and incompatible runtime admission. This port
does not expose public hold endpoints, enable retirement or schedule maintenance.

## Capacity reserved for hold release

An active hold needs capacity for its eventual audited release. Reserve one audit
record, 1,500,000 encoded envelope bytes, 32 outbox deliveries and one retained
identity slot (8,192 logical bytes). These are the existing per-event maxima.
Both administrative and retry holds need the same reservation. The hold row
itself remains a separate used slot.

Use a durable reservation bound to the scoped hold ID and its immutable
placement. Maintain a count of active release reservations under the existing
payload and metadata quota locks. Keep decision reservations in their existing
counters, which enforce a two-event reservation per pending decision. Every
writer and quota update must enforce both reservation classes in addition to
used capacity. With `n` active hold release reservations, require:

| Limit                  | Required remaining capacity after used and decision-reserved capacity |
| ---------------------- | --------------------------------------------------------------------- |
| Audit records          | `n`                                                                   |
| Encoded envelope bytes | `n * 1500000`                                                         |
| Outbox deliveries      | `n * 32`                                                              |
| Retained rows          | `n`                                                                   |
| Retained logical bytes | `n * 8192`                                                            |

Use division of remaining capacity by the per-release bound for admission, so
checking a large or corrupt count cannot overflow. Reserve on successful hold
acquisition in the same transaction as placement and its administrative event.
When releasing, consume only the reservation for that exact hold under the quota
locks before charging the release event. Hold release and its original receipt
must commit with the reservation consumption. Defer the integrity check until
commit so the transaction can consume the reservation before writing the event,
while a partial operation cannot commit. Return unused headroom to the shared
budget. A failed transaction retains the reservation and an exact committed
retry makes no further charge. Release does not refund the historical hold row.

Install and publish the extension explicitly under the deployment writer gate.
Before publication, validate existing holds and account one reservation per
active hold. If any dimension lacks headroom, refuse adoption without changing
readiness. Pinned v4 migrations remain immutable. Activation must prove the
reservation records agree with durable holds and their aggregate count, and the
runtime must refuse incompatible writers before allowing reserved operations.

Acceptance requires TS and Go to prove exact-fit acquire, overflow refusal for
each budget dimension, a release with ordinary writes blocked by full capacity,
and independence from pending decision recovery. Test rollback after reservation
consumption, concurrent acquire/release across languages, and retries following
lost COMMIT responses. Test adoption with active and released historical holds,
insufficient headroom, and a restart between preparation and publication. These
reservations cover logical database quotas. Physical disk exhaustion remains a
storage failure.

Use `heimdall-migrate --prepare-hold-release-upgrade --writers-drained
--sql-directory contracts/persistence/session` for the preparation transaction.
The command requires a compatible v4 primary, verifies the pinned source and
catalogs, and marks the database migrating only after successful adoption.
Exact preparation retries verify the prepared catalog, hold bindings, aggregate
count and headroom. Runtime readiness resumes only after separate activation.

After preparation, use `heimdall-migrate --activate-hold-release-upgrade
--writers-drained` to publish v5. Activation checks the prepared catalog,
reservation coverage and headroom, and the indexed scope and time bindings of
existing holds. An active hold must still point to its charged target payload.
Publication commits the v5 migration checksum with the v5 reader and writer
ranges. Retrying activation validates the published protocol and current ledger.

Use `--status audit-v5` or `--status managed-v5` for exact deployment status.
Current TS and Go runtimes accept compatible v4 and v5 databases. On v5,
acquisition automatically reserves release capacity and release consumes the
matching reservation before charging its audit event. Grant the hold maintenance
role SELECT on `heimdall_retention.hold_release_reservations` and UPDATE on its
`release_event_id` column, alongside its existing hold and audit privileges. Ordinary audit writers need no reservation write access.
