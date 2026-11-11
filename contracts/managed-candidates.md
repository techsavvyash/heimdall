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
