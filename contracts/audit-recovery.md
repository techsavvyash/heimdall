# Durable authorization-decision recovery

This is the implementation protocol for the existing requirement to reconcile
unresolved authorization attempts without inventing historical results. The
prepared `persistence/session/0002.sql` implements its physical decision slot.
It is not yet registered in the supported migration catalog or required by runtime
readiness. Current deployed SDK storage still uses session/audit v1. Do not apply
this file manually to a serving application and claim recovery is enabled.

## Atomic resolution protocol

A decision is identified by product, environment and decision ID. Its durable slot
references the immutable requested event and starts `pending`. The request event,
slot and original outbox deliveries must commit together before evaluation begins.
A receipt for the event alone is insufficient once v2 is activated.

Normal completion and recovery contend on that same pending slot in a local
transaction. Normal completion validates the original request context and writes
its observed authorization.completed event and outbox. Recovery writes
an authorization.completed event with `outcome_unknown` plus the required recovery
administration event, preserving the historical decision's scope/correlation and
identifying the recovery initiator separately. The chosen terminal event ID and
resolved timestamp are installed only while the slot is pending. All events,
outbox entries and the slot transition commit together. Losing contention rolls
back the entire attempted finalization; no unselected terminal event may escape.

The physical states are `pending`, `completed` and `recovered`. Both terminal
states are immutable; neither permits another outcome or a return to pending.
A no-op update is harmless. Request IDs, scope and requested timestamp cannot be
rebound. SQL foreign keys keep request and terminal references within the same
product/environment and prevent those events being deleted while referenced.
The SDK must additionally validate the stored envelope/digest, decision ID,
event type, actor, tenant, target/action/permission, model/manifest/policy and
correlation against the original request. SQL references alone do not prove that
an event is an authorization outcome or that its timestamp matches the event.

If normal completion commits first, recovery observes a terminal slot and performs
no historical reevaluation. If recovery commits first, a late normal completion
must fail to obtain its own terminal receipt. The audited runner must return
`audit_unavailable` and release no allow/deny result. A read-only evaluator may
still finish, but cannot release protected work through that invocation. This is
a local result-release gate; it is not fencing of dispatched remote mutations.
Managed uncertain-write quarantine and verified quiescence remain unchanged.

## Retry, scan and retention requirements

A duplicate event append must compare the complete immutable payload and verify
its existing slot binding before returning the original receipt. A changed payload,
request binding or different terminal event conflicts. A lost COMMIT response is
not rollback: retry/recovery inspects the durable slot and original receipt, never
reruns the old authorization evaluation. Outcome_unknown records do not assert
that a business transaction ran or that the original evaluator returned anything.

The scheduled scanner must use primary reads, fixed product/environment scope,
request-time/decision-ID keyset pagination, a validated caller cutoff, a count
limit and a payload byte budget. It returns validated pending candidates, not a
lease or ownership claim. Every finalization must recheck pending state in its
transaction. An age threshold is an eligibility policy, not proof that a process
is dead. A too-aggressive cutoff can make an active caller fail closed. Scheduling,
queue-age alerts, bounded cancellation and restart checkpoints remain explicit.

Unresolved or held requests cannot be purged by delivered-event retention. The
ordinary runtime role has no DELETE on decision slots. A future authorized
retention transaction must consider the decision pair, all delivery/hold policy,
and retained retry obligations before removing a terminal slot and its events.
Hard audit-capacity policy and terminal/recovery persistence reservations remain
required; the new table alone provides neither backpressure nor reserved space.

## Explicit upgrade and backfill gate

The immutable session/audit v1 SQL remains unchanged. Version 2 must be activated
through a checksum-pinned, serialized deployment upgrade, not runtime DDL:

1. Stop admission and drain all v1 audit writers, including scheduled jobs. Ensure
   old processes cannot resume writing during or after cutover. Startup checks
   alone do not stop already-running processes.
2. Install the additive table/index/guard in the coordinated deployment transaction.
   Keep the database unavailable to v2 runtime until backfill and validation finish.
3. Scan existing audit events in bounded, restartable batches. Decode ha1 and verify
   complete schema/digests before correlating authorization requests and outcomes.
   Preserve all events and outbox receipts; do not rewrite historical payloads.
4. Create exactly one slot per valid requested decision and bind any existing
   matching terminal event. Reject duplicate requests, ambiguous/conflicting
   completions, corrupt envelopes and incompatible context. An unmatched terminal
   is an explicit migration problem, not permission to fabricate its request.
5. Verify full coverage, then publish v2 readiness and reader/writer compatibility
   metadata. Both SDKs must require the activated v2 contract for decision-aware
   append/recovery. The migration command must support inspected retry after lost
   COMMIT and refuse incompatible/checksum-divergent state.
6. Start v2 writers and verify request/evaluation/finalization and recovery races in
   both languages. Keep old writer credentials/processes drained. Do not advertise
   rollback to v1 once new resolution behavior is active.

A bridge/upgrade implementation may separate installation and backfill transactions
for bounded work, but readiness must remain unavailable until coverage is complete.
No automatic repair, silent old-writer coexistence or timer-based backfill shortcut
is allowed. The ordinary v1 initialization mode rejects 0002.sql. The separate preparation
mode below installs the additive files without activating v2 readiness.

## Evidence boundary

`fixtures/audit-recovery-schema.json` pins the prepared SQL checksum and physical
constraint cases. `tests/integration/audit-recovery-schema.py` verifies real
PostgreSQL DDL rollback, scope/retention constraints, terminal immutability,
normal-versus-recovery races, both deterministic resolution orders and transactional
outbox failure. Its routing fixtures are not SDK-valid authorization events.

Required next evidence: explicit upgrade/backfill and corruption refusal; both
SDKs' atomic append/resolve and receipt behavior; crash/commit-loss recovery;
bounded scanning; no late allow release; cross-language parity; audit-capacity and
retention policy. Do not infer those properties from the physical SQL tests.

## Shared decision binding validator

TS `bindAuditDecision` and Go `BindAuditDecision` implement the correlation boundary
for decoded history. They take fixed product/environment and one or two events:
exactly one authorization.requested and zero or one authorization.completed, in
either order. Callers must first verify each ha1 wrapper, stored payload digest and
indexed event binding; these pure functions do not read storage or prove that an
event was durably written.

Both functions validate the full event shape, reserved event semantics, bounded
canonical digest input and SDK source. They compare all request context using the
portable typed-value digest, excluding only event ID/type, outcome, occurred and
recorded timestamps, reason code and SDK version. SDK version records the emitter
and may change across a deployment; source remains SDK. Tenant, verified-actor
fields, target/action/permission, request/decision/operation/parent correlation and
model/manifest/policy versions must match exactly, including optional-field
presence. Canonical object key order does not matter; identity bytes are not
normalized or linked by email.

A terminal must have a different event ID, a recorded timestamp no earlier than
the request's recorded timestamp, and an occurred timestamp no earlier than the
request's occurred timestamp. Duplicate requests, multiple terminal events, an
orphan terminal, malformed events or incompatible context return audit_unavailable.
Invalid configured scope returns invalid_argument. No event is rewritten, merged,
discarded as a duplicate or repaired by this validator.

The result contains only scoped decision/request/terminal IDs, request/resolution
timestamps and the slot resolution: pending without a terminal, recovered for
outcome_unknown, otherwise completed. This classifies recorded history; recovered
does not independently prove a recovery administration event or worker action.
The output is a proposed validated binding, not a committed slot or durable
receipt. Shared positive and negative vectors are in
`fixtures/audit-decision-binding.json`. The bounded deployment backfill below persists these bindings. Global upgrade
coverage/activation and runtime result-release enforcement remain unimplemented.

## Bounded deployment backfill

Internal TS `PostgresAudit.backfillDecisionPage` and Go
`Audit.BackfillDecisionPage` implement one transactional page of historical
binding. They require the prepared decision schema and
`persistence/session/0002-backfill.sql`, an explicitly registered checkpoint for
the configured product/environment, and base metadata in version 1/migrating with
the exact v1 migration ledger checksum. They refuse ready or read-only state.
These files remain outside the supported migration catalog. No public runtime API
or automatic migration is introduced.

The deployment must drain writers and retention jobs before registering scopes.
Every call takes the shared deployment advisory transaction lock, locks its
checkpoint and uses primary reads. Each page contains at most 100 events and
4 MiB of stored event JSON, plus bounded routing/JSON framing. The deadline is
five seconds including connection/lock waits; cancellation before COMMIT rolls
back the page. A first record exceeding the byte budget is an error, never an
empty successful page. Each decoded event must pass the caller's full event
validator and ha1/digest/indexed-binding validation. Products with custom event
registries must supply the matching validator; the present live proof covers
reserved built-in events.

Two C-collated event-ID passes avoid assuming event IDs sort chronologically:

1. `requests`: validate every event and create pending bindings for authorization
   requests. Repeated distinct requests for one decision fail the whole page.
2. At end of the request pass, persist its event count, reset the cursor and enter
   `outcomes`. Validate every event again; bind terminals to the original verified
   request. Orphan, mismatched or multiple terminal events fail the whole page.
3. At end of outcomes, require the same scanned event count before marking the
   scope `complete`. Repeating a completed call returns zero processed events.

Slot changes and checkpoint movement commit together. Neither pass changes
historical events or deliveries, emits replacement events, reruns authorization,
or activates readiness. A lost COMMIT response returns outcome_unknown; the next
call resumes from the durable checkpoint. It does not blindly replay the previous
page. TS and Go can resume one another's checkpoints.

Count agreement detects some source drift; it is not a snapshot or writer fence.
A completed scope does not establish that all database scopes were registered or
that omitted/new scopes are safe. The deployment coordinator still must verify
global coverage and prepared-schema integrity, enforce external quiescence, and
publish v2 compatibility only after all gates pass. Runtime atomic resolution,
recovery scanning, capacity policy and late-allow suppression remain required.

## Prepared atomic decision writer

Internal TS `PostgresAudit.appendDecision` and Go `Audit.AppendDecision` now
persist a normal decision request/completion through the existing audit append
transaction. They are not selected by the public factory yet: v2 upgrade/readiness
activation and the recovery writer remain separate gates. Ordinary generic append
is still available for v1 and other event types; it is not a substitute for this
method after activation.

The request event, original sink deliveries and pending slot commit together.
An exact retry verifies the event digest and original request binding and returns
the original receipt, including when the decision has subsequently terminated.
A different request event for the same decision conflicts and rolls back its event
and deliveries. A request receipt does not authorize repeating an old evaluation.

A completion inserts/verifies its immutable event and deliveries, locks the scoped
decision slot, verifies the original stored request and shared context binding,
and installs the terminal reference only from pending. These operations share one
transaction; the receipt is released only after COMMIT. An exact same terminal
retry returns its receipt. A competing terminal conflicts, and its event/deliveries
roll back. Orphan or corrupt/mismatched history returns audit_unavailable. Generic
payload conflicts remain conflict. Storage/cancellation/uncertain-COMMIT errors
retain the existing distinctions. Audited runners map failure to obtain the
required receipt to audit_unavailable and release no allow/deny result.

Normal writers reject outcome_unknown. A recovery path must also atomically write
the required administration event; this method cannot be used to omit that audit.
The shared append implementation keeps managed callbacks before event writes and
runs decision finalization afterward. Existing workspace-first managed lock order
is preserved; this checkpoint does not combine managed mutation and decision
finalization in one transaction. Go checks primary/read-write connection state
before BeginTx so the driver's explicit transaction mode cannot override a default
read-only connection unnoticed.

`fixtures/audit-decision-writes.json` defines shared outcomes/mismatches/concurrency.
`tests/integration/audit-decision-writes.py` verifies atomicity, reciprocal language
completion, competing terminal rollback, request/terminal COMMIT-loss retries,
outbox failures and existing audited runners suppressing a late allow. The latter
uses a competing normal terminal, not a completed recovery administration path.

## Pending decision maintenance pages

Internal TS `scanPendingDecisions` and Go `ScanPendingDecisions` return validated
pending requests, ordered by requested timestamp then C-collated decision ID.
The inputs are a nonnegative safe-integer inclusive cutoff, an optional cursor and
limit 1–100. A cursor carries productId, environment, cutoff, requestedAt and
decisionId; it must match this configured scope and cutoff and be no newer than
the cutoff. These are trusted maintenance values, not signed public API tokens.
The caller must authorize maintenance access before exposing request payloads.

One primary/read-write SQL statement selects pending slots and original requests
under the same snapshot, with limit+1 lookahead and a 4 MiB aggregate encoded-row
budget. Both SDKs validate each returned row, including lookahead, against the ha1
wrapper/digest, full event validator and shared request binding. Broken binding,
corruption or an oversized first row fails the whole call with audit_unavailable;
nothing is skipped or turned into an empty success. Dependency or primary-mode
failures remain unavailable. The five-second budget includes connection and lock
waits; explicit cancellation preserves the cancellation error.

The result is `{items, next}`. Each item includes decisionId, requestedAt and the
original validated request event. Next is the last returned position only when
count or byte lookahead indicates more; otherwise it is null. Byte-limited pages
may contain fewer than the requested limit with a continuation. All objects are
returned copies; TS snapshots the input cursor before awaiting database I/O.

A page does not claim, lock or resolve a decision. A returned candidate can become
terminal immediately; finalization must recheck pending state transactionally.
Concurrent completion may make a continuation empty. Separate pages are separate
snapshots; requests inserted behind the cursor are found by a later sweep starting
at null. Persist scope/cutoff with a restart position, periodically restart sweeps,
and never treat a completed sweep as proof that all evaluators stopped. Cutoff is
an operator policy, not an automatic death detector or remote-write fence.

`fixtures/audit-decision-scan.json` defines shared order, cutoff and invalid-cursor
cases. `tests/integration/audit-decision-scan.py` exercises live pagination,
reciprocal TS/Go reads, terminal/future exclusion, concurrent completion, corruption,
byte-limited pages, storage oversized-row refusal, read-only refusal, cancellation
and connection-pool reuse. The supported SQL caps each event at 1.5 MB; the
scanner first-row byte-budget refusal is defense against incompatible storage,
not a branch exercised by that live proof. This port
remains internal until v2 upgrade/readiness activation. Scheduler, recovery
administration and capacity/retention policies are still separate requirements.

## Atomic recovery writer

Internal TS `recoverDecision` and Go `RecoverDecision` now accept a proposed
outcome_unknown authorization terminal, a recovery administration event and an
inclusive request-time cutoff. Both events are copied and fully validated before
I/O. The caller must authenticate and authorize the maintenance initiator; event
validation cannot establish that authority. No historical authorization check or
remote mutation is executed by this port.

The terminal retains the original decision context/actor and passes the shared
request/outcome binding validator. Its occurredAt equals recordedAt, representing
recovery time rather than pretending to know evaluation time. The administration
event has type heimdall.recovery.completed, outcome succeeded, source sdk, a known
user/system actor identifying the recovery initiator, the same scope/tenant and
decisionId, target `{type: "authorization_decision", id: decisionId}` and action
`authorization.recover`. Its occurred/recorded time equals the terminal time.
Its requestId belongs to the recovery request; the historical terminal preserves
the original requestId. Both retain ordinary event schema/semantic restrictions.
The terminal time must be at least the cutoff; stored requestedAt must be no later
than it. Too-recent candidates conflict without writes. A cutoff is an eligibility
policy, not proof an evaluator is dead.

There is exactly one possible administration event ID for a scoped terminal:
`recovery-` plus lowercase SHA-256 hex over UTF-8 bytes of
`heimdall.recovery.v1\0{productId}\0{environment}\0{terminalEventId}`.
The separators are NUL bytes; configured scope/event IDs cannot contain NUL. The
terminal and administration IDs must differ. This deterministic binding prevents a
retry from selecting another administration ID or silently changing the initiator.
The shared fixture pins an independently calculated ID. Retention must treat this
linked administration event as part of the decision pair; it is not independently
purgeable while recovery receipt/retry obligations remain. No retention DELETE is
added here.

The transaction writes/verifies the terminal and original sink deliveries, locks
and validates the original request/slot, installs recovered only from pending,
and writes/verifies the administration event and its deliveries. It releases both
receipts only after COMMIT. Failure in any part rolls back the whole attempt.
Normal completion and other recoveries use the same slot. A different winner
returns conflict and leaves no losing event or administration record. An exact
retry verifies both original payloads and returns both receipts without duplicates.
A changed administration payload conflicts. If a matching already-recovered slot
has no administration event, recovery fails audit_unavailable rather than creating
one retroactively. Imported historical unknown outcomes therefore do not acquire
fabricated proof of a recovery initiator.

Lost COMMIT returns outcome_unknown; retry the same pair and inspect durable
receipts, never rerun the old evaluation. The existing audited runners fail closed
when recovery wins before their normal finalization; no late allow is released.
This is local result gating, not a fence for previously dispatched remote writes.

`fixtures/audit-recovery-write.json` and
`tests/integration/audit-recovery-write.py` prove pair validation, normal/recovery
race orders, competing recoveries, rollback, exact retries, missing-administration
refusal, cross-language recovery and committed-response loss. Factory selection,
v2 deployment upgrade/readiness, scheduling, capacity and retention remain open.

## Explicit offline preparation command

The Go deployment CLI now accepts one additional mutually exclusive mode:

```sh
heimdall-migrate --prepare-audit-upgrade \
  --sql-directory contracts/persistence/session --writers-drained
```

It reads HEIMDALL_MIGRATION_DATABASE_URL as before. The directory must contain the
exact pinned 0002.sql, 0002-backfill.sql and 0002-upgrade.sql files; altered bytes
are rejected before database I/O. The flag is an operator assertion that all old
writers, background jobs and retention have stopped and cannot resume. It does
not stop processes, revoke credentials or prove quiescence. Do not run it on a
serving application or treat the incomplete upgrade flow as production-ready.

PrepareAuditUpgrade uses a primary/read-write connection, a 30-second overall
budget, five-second lock and 20-second statement timeouts, synchronous commit and
the shared deployment advisory lock. From ready v1, it atomically installs the
prepared tables/guard, writes a singleton deployment receipt with all checksums,
and changes base metadata to migrating. Version and the v1 migration ledger stay
unchanged. The new receipt is deployment-owned; no runtime table creation or
permission to change readiness is inferred.

Failure before COMMIT rolls back the entire installation and metadata change.
Cancellation during lock acquisition leaves ready v1 intact. Concurrent callers
serialize; retry inspects the committed receipt, exact migration metadata/ledger
and required table presence rather than rerunning DDL. It refuses unrecognized
partial objects, mismatched receipts and incompatible metadata. A lost COMMIT
response is outcome_unknown and permits this same inspected retry. Neither retry
nor the receipt asserts full physical-schema integrity; activation must verify
that separately, together with complete global history coverage.

The command explicitly prints that runtime remains unavailable. It does not
register scopes, run backfill, publish v2 compatibility or start writers. The v1
runtime/status checks refuse migrating state; already-running old processes still
require the external drain. Catalog versions, public SDK factories and their
ready-v1 policy are unchanged. SQL checksum vectors and live evidence are in
fixtures/audit-upgrade-prepare.json and tests/integration/audit-upgrade-prepare.py.
