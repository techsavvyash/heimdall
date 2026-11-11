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
is allowed. The current migration CLI intentionally does not accept 0002.sql yet.

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
