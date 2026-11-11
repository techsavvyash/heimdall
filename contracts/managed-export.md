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
