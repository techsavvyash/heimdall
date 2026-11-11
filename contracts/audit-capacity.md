# Audit capacity admission and decision reservations

This implements the arithmetic behind the selected audit hard-limit requirement.
It is an internal shared contract, not an enforced PostgreSQL admission gate yet.
`fixtures/audit-capacity.json` is executed by both SDKs. Deployment installation,
existing-history accounting, atomic persistence and runtime composition remain
required before this can protect a serving deployment.

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
input immutability. This proves arithmetic parity only. Atomic race, rollback,
COMMIT-loss, migration and public fail-closed tests remain required for enforcement.
