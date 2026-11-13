# Managed candidate source

Internal TypeScript `CandidateRecords.scan` and Go `CandidateRecords.Scan` return
scoped discovery hints from PostgreSQL. They are not public discovery APIs and do
not authenticate the supplied identity, check OpenFGA or produce audit decisions.
The future discovery layer must validate identity, authenticate cursor scope,
perform current lifecycle/membership checks and persist required decision audit.

The source captures product/environment, computes the canonical principal and its
identity hash, and resumes strictly after a canonical unsigned decimal position.
`"0"` starts traversal; positions and revisions stay decimal strings through both
SDKs, including values above the JavaScript safe-integer range. The maximum is
PostgreSQL's positive signed bigint. Input limits are integers from 1 through 100.
Malformed input returns invalid_argument before database access.

One five-second-bounded primary SELECT joins the principal's candidates to scoped
active, unblocked workspaces, orders by numeric candidate position and returns at
most limit + 1 rows. The outer aggregate also returns the primary/read-write status
when no candidate matches. Replica/read-only status, query failure, cancellation
and deadline failure cannot be reported as a successful empty page. Runtime does
not migrate, mutate, acquire a lease or cache authorization in this method.

Each row must match the canonical principal/hash and expected product/environment.
Workspace fields pass the existing strict workspace decoder; active/unblocked state
is checked again. Positions must strictly increase from the supplied position,
workspace IDs must be unique in the batch, revisions must be positive bigint
strings, and observation timestamps must be safe nonnegative integer milliseconds.
The lookahead row is validated too. Any malformed row fails the whole page as
unavailable rather than silently dropping it or advancing past it.

A page contains at most limit items, each with its source position, candidate
revision, observation time and validated workspace snapshot. `nextPosition` is null
when this snapshot has no lookahead; otherwise it is the final returned item's
position, never the lookahead position. A caller that consumes only part of a batch
must resume from the last item it actually consumed. The discovery layer's public
page limit and scan budget apply separately to membership checks.

This is live keyset traversal, not a snapshot across requests. Lifecycle changes,
new commits behind the cursor and formerly inactive workspaces can require a fresh
traversal. Updates preserve candidate position; a removed/recreated candidate can
have a new position. An empty page says nothing about complete remote membership.
No totals are exposed. Source positions must be wrapped in the existing signed,
principal/product/environment/model-bound cursor before reaching clients.

The query bounds returned rows and elapsed time, not the number of database rows
examined while filtering inactive workspaces. Operators still need indexing and
capacity monitoring. Candidate rebuild coordination, reconciliation and authenticated
public discovery remain separate implementation requirements.

Shared decoder cases: `fixtures/candidate-page.json`. The managed PostgreSQL harness
proves pagination, scope isolation, lifecycle changes, cancellation and reciprocal
TS/Go reads, including candidates written by completed managed operations.

## Guarded repair batches

Internal `OperationRecords.repairCandidates` / `RepairCandidates` applies 1..100
trusted membership observations to one known active, unblocked workspace. Capture
its revision **before** obtaining the observations. Each entry supplies an exact
identity, member boolean and observation timestamp; duplicate identities and invalid
bounds fail before I/O. A false value requires an explicit verified non-membership
result, not absence from an incomplete list. The method does not query OpenFGA or
validate the remote evidence; future orchestration must do so and audit decisions.

The five-second transaction updates the workspace at the expected revision before
locking candidate rows. It rejects stale revisions, inactive/blocked workspaces,
observation times older than the workspace's previous update, and existing candidate
observations newer than the incoming result. Every successful batch advances the
workspace revision, even when its hint changes are logically idempotent. This
serializes cooperating rebuild batches with SDK membership reservation, cancellation
and completion. A stale batch cannot simply be retried with a new revision: capture
new state and obtain new observations first.

Positive observations upsert canonical candidates while preserving existing source
positions. Explicit negatives delete only their specified identity. Unmentioned
candidates remain unchanged. Existing principal/hash inconsistency conflicts rather
than being silently overwritten. The workspace remains active and unblocked; repair
never clears quarantine, changes relationships or creates an unknown workspace.

The candidate changes, new workspace revision and required recovery audit/outbox
record commit together. The event has a fixed system actor candidate-rebuild,
action candidate.repair and empty attributes. Audit failure rolls back the whole
batch. A lost COMMIT acknowledgement returns outcome_unknown without a success
result. Its old revision cannot be replayed after a committed repair. This does
not provide operation-key deduplication or permit reusing old remote observations.

The operator uses `rebuildDiscovery` / `RebuildDiscovery` to process one page of a
known active workspace. Each call first captures the workspace revision and
verifies the pinned model. The remote pass enumerates direct member tuples and
publishes positive observations. The candidate pass uses stable numeric positions
and checks each existing candidate with higher consistency before removal. A
missing tuple in a listing does not supply a negative observation.

The returned checkpoint is bound to product, environment, workspace, store,
model and manifest. Both SDKs use the same JSON shape. Persist it in trusted
operator storage only after a successful response. Retry from the previous
checkpoint after a conflict, cancellation, or unknown outcome. Each retry obtains
fresh observations and a fresh revision. A null checkpoint marks the end of the
sweep. A fresh sweep can observe out-of-band changes that landed behind a cursor.
Authorization remains subject to fresh relationship checks throughout the repair.

Shared repair inputs: fixtures/candidate-repair.json. The managed PostgreSQL harness
covers partial batches, stale revisions/timestamps, mutation races, audit rollback,
TS/Go reads and committed-response-loss behavior.

## Retained runtime read composition

TypeScript `WorkspaceRecords.forRetainedRuntime` and
`CandidateRecords.forRetainedRuntime`, and Go `NewRetainedWorkspaceRecords` and
`NewRetainedCandidateRecords`, require audit-v4 and managed-v1 readiness in the
same primary SQL snapshot as every result. The query checks both schema families
before returning absence or an empty page as success. Migrating/legacy protocols,
incompatible ledgers and read-only transactions fail unavailable. A SELECT-only
role can read when it has access to the readiness tables and requested data; a
read-only PostgreSQL transaction remains intentionally incompatible with runtime
admission.

Constructors do no I/O. These factories preserve scope, lifecycle filtering,
validation of every row including lookahead, decimal bigint positions, five-second
bounds and cancellation. They do not mutate or generate authorization decisions.
Candidate hints still require current membership checks at the future discovery
boundary. The legacy internal constructors remain available for migration tests;
runtime callers select the retained factories explicitly.

The shared managed-retained-runtime fixture now covers these reads alongside
operations. The activated database harness runs the full workspace/candidate
fixtures and reciprocal reads, including hints produced by retained operation
completion and repair.
