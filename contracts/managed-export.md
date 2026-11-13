# Sanitized managed metadata export

Internal TS `ManagedExport.scan(kind, workspaceId, after, limit, signal?)` returns
JSON text. Go `ManagedExport.Scan(ctx, kind, workspaceID, after, limit)` returns
JSON bytes. These are read-only storage ports, not public operator endpoints.
Callers must authorize the exact product/environment and tenant selection and
record required export-administration audit before releasing an artifact.

The constructor fixes product/environment. `kind` is `workspaces` or `operations`.
Workspace export accepts a null filter for an authorized whole-product inventory
or an explicit workspace ID. Operation export requires an explicit workspace ID.
The nullable `after` position is the last emitted ID, not a signed public cursor or
an authorization capability. IDs use the existing ASCII identifier grammar. Limit
is an integer from 1 through 100. Calls have a five-second deadline and preserve
cancellation; no workers, migrations or remote calls run implicitly.

Each version-1 JSON page contains `schemaVersion`, `productId`, `environment`,
`kind`, `workspaceId` (the filter or null), `items` and `nextId` (or null). JSON
object key order is not part of the format. Both languages emit the same value
types. Every item includes product/environment/workspace IDs and safe-integer
`createdAt`/`updatedAt` milliseconds. Additional fields are:

| Kind       | Additional fields                                                                                                                    |
| ---------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| workspaces | `lifecycle`, `mutationBlocked`, `revision` (exact positive signed-bigint decimal string)                                             |
| operations | `operationId`, `kind`, `state`, `dispatchCount`, `generation`, `nextAttemptAt`, `storeId`, `modelId`, `modelHash`, `manifestVersion` |

Pending, active, suspended and tombstoned workspaces remain visible. All operation
states, including terminal and uncertain, remain visible. Export never interprets
uncertainty as success or clears a lifecycle gate. It validates metadata types,
scopes, bounds, state/count constraints, ordering and timestamps, including the
lookahead row; malformed metadata fails the entire page. This is not full journal
integrity verification: intent payloads and their bindings are deliberately not
read, and historical model pins are reported rather than required to match the
current runtime model.

Queries project an explicit column allowlist. They do not select workspace names,
identity hashes, principals, intent payloads, fingerprints, idempotency digests,
role grants, candidates, audit payloads or sessions. A restricted database role
with schema USAGE and SELECT only on those metadata columns can execute the port;
no UPDATE, DELETE, encryption keys or sensitive-column privileges are needed.
IDs may themselves carry operator-selected identifying information. Sanitized
means the listed fields only, not anonymous or suitable for public disclosure.

Each call reads one primary snapshot, using C-collated ASCII keyset ordering and
at most `limit + 1` records. Primary/read-write connection status is checked even
for an empty page; the port itself writes nothing. Fixed field lengths bound each
page independently of potentially large intent payloads. A continuation points
to the last emitted item, never the unreturned lookahead. A later page sees a new
snapshot: concurrent changes or inserts before its position can be omitted from
the overall traversal. For a consistent exit inventory, stop and drain all
participating writers before paging; this port does not establish that freeze.

## Model and relation mapping for exit

Preserve the exact manifest and compiler output for every referenced store/model
hash/version. `compileTenantModel`/the Go tenant-model compiler produces the
role/permission mapping: `member` is explicit workspace membership; `r_N` is a
role assignment; `e_N` is membership-intersected effective role; `d_N` is the
member-gated delegation relation; `p_N` is a member-gated permission. Ordinals
come from sorted names in that exact manifest, not stable global role IDs. Never
infer an old relation's business meaning from a different manifest. Preserve the
owner-role mapping, workspace object IDs and canonical issuer/subject user
encoding. Retrieve the pinned model and relationship data separately through
OpenFGA's supported APIs, under separately authorized export access.

This metadata file cannot restore sessions, identity denials, audit history,
idempotency obligations or relationships. Backups remain separate, access-controlled
artifacts. Before exiting managed mode, resolve dispatched/uncertain writes using
the approved quiescence procedure and explicitly transfer lifecycle and recovery
responsibility. Export success does not certify those prerequisites. Restoring
PostgreSQL independently must keep managed authorization disabled until state is
reconciled; never replay an exported or restored backlog automatically.

Shared tests: `fixtures/managed-export.json`; live column-privilege, pagination,
scope, cancellation and reciprocal TS/Go proofs: `tests/integration/managed-schema.py`.

## Retained runtime admission

TypeScript `ManagedExport.forRetainedRuntime` and Go `NewRetainedManagedExport`
select audit-v4/managed-v1 runtime admission without constructor I/O. They evaluate
both schema families in the same primary SQL snapshot as the allowlisted export
rows. Legacy/migrating audit state, incompatible managed metadata or ledger, and
read-only connections fail unavailable even for empty pages. The original internal
constructors remain explicit legacy-schema diagnostic/migration ports.

The retained export role additionally needs SELECT on the schema metadata and
migration ledgers plus audit capacity/retention policy, upgrade and backfill
receipts used by readiness. It does not need SELECT on audit payloads, compact
event identities, sessions, names or intent payloads. The explicit managed-column
projection and portable page format remain unchanged. Required authorization and
export-administration audit still belong to the caller before artifact release;
readiness is not an export permission or a consistent whole-inventory snapshot.

The activated profile in `tests/integration/audit-capacity-backfill.py` now runs the
shared export fixtures, restricted-column reads, reciprocal language reads,
readiness refusal, cancellation and post-timeout reuse through retained factories.

## Authorized page release

The internal TS `runManagedExport` and Go `RunManagedExport` compose the retained
export reader with the deployment's audited authentication and policy. The
deployment supplies the pinned model and manifest version, the trusted reader,
and a policy that evaluates the verified actor and exact product, environment,
workspace and export kind. A whole-product inventory requires an explicit grant
for the null workspace. Operations always require a named workspace.

Each invocation persists the authorization request and outcome before accessing
the reader. It checks that the returned page header matches the authorized scope,
then records `heimdall.export.completed` with the actor, policy, scope, export kind
and operation ID. A read failure records a failed outcome with a typed reason.
The page stays private until a matching audit receipt commits and the identity
passes a final local deny, expiry, freshness and cancellation check. A successful
completion records that a page was prepared for release. Transport delivery can
still fail. Each retry is a fresh authorized read with its own audit records.

The product owns the policy implementation, transport and artifact destination.
For a consistent multi-page exit inventory, the product drains writers before
the first page and keeps them drained until the final page. Page reads use the
restricted export credential. Authorization and completion use the separate
retained audit writer.
