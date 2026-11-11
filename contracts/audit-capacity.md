# Audit capacity admission and decision reservations

This implements the arithmetic behind the selected audit hard-limit requirement.
It is an internal shared contract, not an activated public admission gate yet.
`fixtures/audit-capacity.json` is executed by both SDKs. Offline preparation is
implemented along with bounded historical accounting; activation and public
runtime composition remain required before this can protect a serving deployment.

## Units and bounds

A policy has explicit positive limits for event rows, encoded envelope bytes and
outbox delivery rows. Each limit is an integer no greater than 9007199254740991,
so TypeScript and Go compare the same exact values. There is no unlimited default.
Counters distinguish committed usage from reserved future usage. Every dimension
must satisfy `used + reserved <= limit` without overflowing the shared integer
range. Invalid policy or charge is `invalid_argument`; inconsistent counters or
insufficient capacity are `audit_unavailable`.

Envelope bytes mean PostgreSQL `octet_length(event::text)` of the stored ha1 JSONB
envelope. They are not JavaScript string length, raw event payload length, logical
digest size or PostgreSQL physical disk allocation. The existing immutable SQL
caps each envelope at 1500000 bytes; each SDK supports at most 32 sinks per event.
Indexes, table overhead, decision slots, WAL, vacuum space and replication need
separately monitored physical capacity and operational headroom. A logical budget
cannot guarantee that an unhealthy or full disk will accept a completion.

## Transitions

| Transition            | Committed usage added                                     | Reservation change                                     |
| --------------------- | --------------------------------------------------------- | ------------------------------------------------------ |
| Ordinary append       | One event, its actual envelope bytes and delivery rows    | None                                                   |
| Authorization request | One event, its actual envelope bytes and delivery rows    | Reserve two events, 3000000 bytes and 64 delivery rows |
| Normal completion     | One event, its actual envelope bytes and delivery rows    | Release that decision's entire reservation             |
| Recovery completion   | Two events, their actual combined bytes and delivery rows | Release that decision's entire reservation             |

Recovery includes the unknown-outcome terminal plus the required administration
event. Reserving the full supported bounds at request admission means a later
valid completion/recovery can fit even if ordinary new work has filled all other
logical capacity. Normal completion releases its unused recovery allowance.
Remaining reservation counters must represent whole two-event/3000000-byte/64-row
units and cannot outnumber committed request events. Ordinary appends cannot
borrow reserved capacity.

This is conservative: even a small request with no configured sinks reserves the
maximum supported recovery pair. Changing that bound requires a versioned contract
and migration of outstanding reservations, not a runtime configuration shortcut.
Very small valid limits may admit ordinary events but refuse all decision requests.
No result is exempt because it would deny access or report a dependency failure.

## Required persistence composition

The pure transition takes a snapshot and returns new counters; it neither performs
I/O nor establishes transaction ownership. The database implementation must:

1. Serialize admission against the policy and bind one reservation to the scoped
   decision/request. One decision cannot consume another decision's reservation.
2. Deduplicate the exact stored event payload and existing decision binding before
   charging. A retry is not a second event or a second reservation. Changed payloads
   and conflicting terminals remain conflicts, not capacity errors.
3. Compute actual envelope charges using the database's JSONB byte representation.
   Count the original actual outbox rows; a retry cannot silently add new sinks.
4. Commit counters, events, outbox rows and decision-slot transitions atomically.
   Rollback leaves every component unchanged. Lost COMMIT requires inspecting the
   existing event and reservation, not assuming rollback or refunding capacity.
5. Require compatible initialized policy at startup and each mutation boundary.
   Existing history and pending requests must be accounted for under an explicit
   drained migration before new capacity-aware writers can be activated.
6. Release committed usage only through authorized retention after validating every
   delivery, hold, unresolved-decision and retry obligation. Acknowledgement alone
   does not free local storage. Recovery must not lose its administration record.

Counters and reservations do not fence remote mutations or revive already-recovered
decisions. Managed lifecycle quarantine and the existing recovery binding protocol
remain mandatory. Capacity reservations for managed mutation workflows and their
attempt/recovery records require composition with those workflows; these decision
transitions do not certify that later release requirement.

## Evidence boundary

Shared vectors cover exact admission limits, one-unit shortfalls in each dimension,
normal-completion release, maximum recovery at full capacity, ordinary append
refusal, missing/partial reservations, corrupt counters, invalid charges and safe
integer boundaries. TS additionally tests nonfinite/fractional numeric inputs and
input immutability. Those vectors prove arithmetic parity only. The prepared physical and SDK proof
below adds atomic races, rollback and COMMIT-loss coverage. Migration and public
fail-closed tests remain required for deployed enforcement.

## Prepared PostgreSQL capacity ledger

`persistence/session/0003-capacity.sql` is a checksum-pinned prepared addition,
not an activated migration in the compatibility catalog. It requires the v2 audit
and decision tables. Serving writers must be drained before installation. The
explicit preparation step below initializes policy; historical accounting and
compatible runtime activation are still required. Policy starts `preparing`.

One policy row bounds the entire audit database across products/environments. This
prevents separate per-product budgets from silently exceeding that database's
logical limit, at the cost of shared admission contention and availability. It is
not a tenant quota or a physical disk budget across multiple PostgreSQL databases.
Scoped event charges and decision reservations retain product/environment keys.

Event-charge insertion derives the exact JSONB text byte count and original outbox
row count from stored data. AFTER INSERT accounting avoids charging `ON CONFLICT
DO NOTHING` retries. Charge identity/amounts are immutable. Outbox insertion and
charging lock the same policy row; adding a sink after an event is charged is
refused, and outbox identity columns cannot be rebound. Mutable delivery status
still works. These locks require no UPDATE permission on audit history.

A pending reservation must match its decision's request and reference a charged
request. It adds the fixed recovery bound through a trigger. Resolution must match
the decision slot. Its terminal charge, plus its administration charge for recovery,
is required through scoped deferred foreign keys at commit. This permits releasing
reserved capacity before charging the selected outcomes in the same transaction,
without permitting a committed release that omits those charges. Resolved bindings
are immutable, no-op updates do not release twice, and pending reservations cannot
be deleted. Direct policy-counter resets and policy deletion are refused.

The runtime role has no charge/reservation DELETE, policy-limit update or DDL
privilege. The physical decrement on charge deletion is reserved for a future
privileged retention transaction; it does not itself verify delivery/hold/age or
retry obligations and is not a supported purge API. Operators are trusted and can
change deployment policy; this is not isolation from a hostile database owner.

Physical tests prove DDL rollback, server-derived charges, constraint refusals,
transaction rollback, maximum recovery at full capacity, concurrent cross-product
admission, normal/recovery contention and both sink/charge race orders. Their SQL
events validate routing constraints only, not complete SDK event schemas. The prepared SDK paths below
validate full ha1 payload/digests, semantic event/decision/admin bindings,
and original retry receipts before invoking accounting. Unaccounted legacy writes
remain possible until the new runtime protocol, complete backfill and activation
gate are implemented. This prepared schema is not a public enforcement claim.

## Prepared SDK transaction paths

The internal TS `appendCapacity` / `recoverCapacity` and Go `AppendCapacity` /
`RecoverCapacity` paths now compose complete event validation, decision binding,
physical accounting and original receipts in one transaction. They are not public
factory entry points. The test harness installs an empty prepared database and
sets its policy ready; this is not a supported historical migration or activation.

The writer takes shared deployment admission, then the primary policy row lock,
before inserting events or locking decisions. Policy must be ready on every
attempt, including retries. Ordinary append charges one new event. Request append
also creates its scoped pending reservation. Normal/recovery completion validates
and updates the decision, releases that reservation, and charges the selected
terminal (plus recovery administration) before COMMIT. Deferred charge references
and the transaction boundary prevent a partial release. A failed outcome or sink
insert rolls everything back.

An exact retry validates the existing ha1 payload/digest, charge bytes and original
outbox count, and verifies reservation/decision bindings without inserting another
charge or reservation. Existing uncharged events and missing reservations fail
closed; runtime does not backfill them. A changed sink configuration does not add
new destinations to an existing event. Conflicting payloads or losing terminal
choices remain `conflict`; capacity exhaustion and incomplete accounting are
`audit_unavailable`. An unconfirmed COMMIT remains `outcome_unknown`; an identical
retry inspects the committed state and cannot refund or double-charge it.

`fixtures/audit-capacity-writes.json` drives both live suites, with a three-event
budget and separate byte/delivery shortfall cases. Proof includes normal/recovery
races, cross-product admission, both cross-language recovery directions and lost
COMMIT responses for ordinary/request/completion/recovery writes. Public startup,
a versioned migration/backfill/activation gate, managed mutation reservations and
authorized retention remain required before claiming complete enforcement.

## Offline upgrade preparation

The deployment executable `heimdall-migrate --prepare-capacity-upgrade` now installs
`0003-capacity.sql` and `0003-upgrade.sql` from `--sql-directory`. It requires
`--writers-drained` and explicit positive `--capacity-events`, `--capacity-bytes`
and `--capacity-deliveries` limits in the shared safe-integer range. Its database
comes from `HEIMDALL_MIGRATION_DATABASE_URL`. The CLI serves deployments using either
SDK; no additional running service is introduced.

Preparation validates the activated v2 metadata, original upgrade receipt and
exact PostgreSQL 17 catalog profile before adding the capacity objects. Within one
synchronous transaction under exclusive deployment admission, it installs the
pinned SQL, initializes a preparing policy, stores the requested limits/checksums,
creates an empty backfill checkpoint, and changes v2 state to `migrating`. It then
checks the extended structural profile, including both schema identities and the
new audit foreign keys/triggers. No v3 migration or runtime readiness is published.

An existing capacity namespace is not adopted as a fresh install. Exact retries
require the prepared receipt, matching policy, complete structural profile and
unchanged requested limits. They preserve already committed charges and backfill
progress. Changed requested limits are a conflict; independently altered policy
or metadata is refused. A lost COMMIT is `outcome_unknown` and requires the same
command to inspect its receipt. Mid-DDL failure rolls back both schema and metadata.
The operation has a 30-second deadline, 5-second lock timeout and 20-second statement
timeout. Operator quiescence remains an external assertion; these locks cannot
fence a stopped process that is later restarted with an old unrestricted port.

`0003-upgrade.sql` extends the prepared reservation triggers so offline backfill
can insert an already completed/recovered binding while policy is `preparing`.
Such a binding consumes no future reservation and retains all scoped decision and
charge foreign keys. Pending imports reserve the full recovery bound. Once policy
is ready, new resolved imports are rejected. The old pinned SQL is unchanged.
This SQL mechanism does not validate historical event semantics; the backfill
runner must decode and validate every request/terminal/recovery administration
before using it, without fabricating missing recovery records.

The checkpoint records event and decision passes, a global product/environment/ID
cursor, and separate scanned counts. Creating it is not performing the backfill.
The bounded runner below performs semantic accounting and coverage verification.
Activation and public protocol composition remain outstanding. Preparation
intentionally leaves a serving outage until the complete upgrade workflow exists.

## Bounded historical accounting

`heimdall-migrate --backfill-capacity-upgrade --writers-drained --batch-size N`
advances one durable page using the deployment connection. `N` is 1 through 100
(default 100). The programmatic deployment runner accepts a full event-validation
callback; the current CLI accepts reserved built-ins only and refuses custom
history until registry selection is implemented. This shared deployment tool
accounts records written by either SDK; it does not run as a service.

Each call checks primary/prepared compatibility and the pinned extended schema,
takes exclusive deployment admission and locks the checkpoint. It uses a 10-second
operation deadline, 5-second lock timeout and 5-second statement timeout. All
writers, export mutation and retention jobs remain externally stopped.

- The event pass walks product/environment/event ID in C collation order. At most
  100 candidates and 4194304 returned envelope bytes are processed. Every ha1
  payload/digest is validated; authorization records must match a decision's
  request or terminal ID. Server-derived charges are inserted or verified without
  rewriting audit events or outbox routing.
- The decision pass walks product/environment/decision ID. It fully validates the
  request/terminal pair and slot fields. Recovered decisions must also have their
  deterministic, correctly bound administration record; missing records are not
  fabricated. Its returned envelope budget is 6291456 bytes, conservatively
  including the maximum 1500000 bytes for each separately fetched recovery
  administration. This permits at least one maximum-size three-event history.
  The same 100-candidate cap applies; transport metadata is additional overhead.
- Pending imports reserve the full recovery allowance; resolved imports add none.
  Existing imports must match every nullable binding. Each page's changes and
  checkpoint commit together. An invalid later record or insufficient capacity
  rolls back that whole page while retaining prior committed progress.

A lost page COMMIT returns `outcome_unknown`. Running the command again reads the
stored checkpoint: it repeats an uncommitted page or advances after a committed
one, without charging or reserving twice. It does not promise to return the lost
page's exact progress response. Concurrent deployment processes serialize through
the same admission lock.

Before moving to the decision pass, event counts and charge coverage must match.
Before reporting `complete` (and on subsequent complete calls), the runner checks
scanned counts against actual events/decisions, missing or mismatched charges and
reservations, actual encoded sizes and original sink counts, and aggregate policy
usage/reserves. Late unaccounted history or a skipped cursor refuses completion.
These coverage scans remain subject to the deadline; large-history qualification
is still required. Completion does not change metadata, policy readiness or the
migration catalog. Activation must separately revalidate and publish a compatible
public protocol.

Fresh preparation now checks that configured limits can contain all existing
events, encoded bytes and deliveries plus every pending decision's recovery bound.
These checks run under the old-profile table locks before capacity DDL or migrating
state is committed. Insufficient limits fail while original v2 readiness remains
intact. This is logical sizing, not proof of payload validity or physical disk
headroom. Independent manual changes after preparation remain unsupported; the
backfill still refuses over-budget accounting rather than publishing partial
reservations.

## Atomic activation transaction (internal deployment primitive)

`ActivateAuditCapacityUpgrade` rechecks the pinned extended schema and prepared
receipt under exclusive deployment admission and table locks. A completed
checkpoint must match full charge/reservation coverage before semantic validation
starts. It then revalidates every stored envelope, decision pair and recovered
administration using the bounded backfill readers, without changing the durable
checkpoint. Missing accounting is an error, not an implicit repair.

Only after validation succeeds does one synchronous transaction insert migration
3 (the pinned capacity SQL checksum), set the capacity policy to `ready`, and
publish database/reader/writer version 3. Failure before COMMIT leaves the prepared
state intact. An uncertain COMMIT returns `outcome_unknown`; an exact retry checks
the activated schema, receipt, policy and protocol without comparing historical
checkpoint counts to legitimate post-activation writes.

The operation has a 30-second overall deadline, a 5-second lock timeout and a
20-second statement timeout. Its full historical scan is not resumable and remains
unqualified for large histories. External writer drain remains required.
This primitive is internal. The deployment CLI and readiness policy below now
expose its storage protocol; public TS/Go composition below now requires v3. Legacy v2
factories reject v3.

## Deployment CLI and v3 readiness

`heimdall-migrate --activate-capacity-upgrade --writers-drained` now invokes the
activation transaction. The explicit drain assertion and single-mode validation
are required; preparation limits, SQL input and batch flags are rejected in this
mode. The CLI uses `HEIMDALL_MIGRATION_DATABASE_URL` and the built-in-only validator
shared by previous upgrade commands. Custom-event registry selection remains a
separate release dependency.

`--status audit-v3` checks the activated protocol. `--status managed-v3` checks
both audit-v3 and managed-v1 in one statement snapshot. TypeScript
`checkCapacityReadiness`/`checkManagedCapacityReadiness` and Go
`CheckCapacityReadiness`/`CheckManagedCapacityReadiness` share the same policy:
primary writable database, exact version/reader/writer 3, all three migration
checksums, one ready policy, matching preparation receipt and limits, and a
completed cursor-free checkpoint. Historical checkpoint counts are not live
usage counters and are not compared with current history by startup readiness.
Missing tables or incompatible state return unavailable; cancellation is retained.
These startup checks do not replace structural validation at deployment.

Explicit managed-schema initialization accepts the ready v3 base. If a capacity
namespace exists it must satisfy v3 readiness, rather than falling back to an older
base protocol. The shared catalog now records migration 3 as
`session/0003-capacity.sql`; its preparation extension remains checksum-bound by
the upgrade receipt. SQL files are immutable.

Public factories now require v3 at startup and for each audit append, including
exact receipt retries. They hold shared deployment admission through COMMIT and
use the capacity-accounted request/completion path. New ordinary events are also
charged. Recovery-pair ports remain internal pending maintenance orchestration.
See core-runtime.md for the runtime grants and public-runtime-v3.py for live proof.
Storage readiness is not complete product release qualification.
