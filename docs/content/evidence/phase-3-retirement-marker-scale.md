---
title: Phase 3 retirement marker lookup at scale
date: 2026-11-13
---

TypeScript and Go now fetch up to two marker matches once per retained event
lookup. A second match still fails inspection. The check for surviving payloads
uses the event and decision member keys against the audit event primary key.

```mermaid
flowchart LR
  Lookup[Read one scoped retained event] --> Markers[Fetch at most two marker matches]
  Markers --> Integrity{Marker count agrees with payload state}
  Integrity -->|No| Refuse[Return audit unavailable]
  Integrity -->|Yes| Verify[Validate bindings and check exact member keys]
```

Run `heimdall-marker-scale-7a6424aed58843f18489d382199f1157` used pinned
PostgreSQL 17.11 and synthetic retirement markers for two products in the same
environment. The probe uses the existing retirement table definition with
fixture identity keys and measures the scoped marker predicate in isolation.

| Lookup | Markers per product | Rows filtered | Shared blocks | Execution time |
| --- | ---: | ---: | ---: | ---: |
| Miss with existing schema | 1,000 | 2,000 | 35 | 0.555 ms |
| Miss with existing schema | 100,000 | 200,000 | 3,818 | 58.705 ms |
| Miss with candidate GIN index | 100,000 | 0 | 4 | 0.242 ms |
| Hit with candidate GIN index | 100,000 | 1 | 5 | 0.397 ms |

The candidate uses `gin(member_event_ids)` with the default JSONB operator
class, which supports the existing membership predicate. Identical member IDs
in different products still yielded one scoped match. A second match inside
the same product yielded two. These are single-query measurements on a local
fixture. They establish the scan cost and index access path. Application
latency and behavior under concurrent write load need separate measurements.

Reproduce with `python3 tests/integration/retirement-marker-scale.py` on a
Docker host with space for 200,000 markers and 400,000 identity keys. Set
`HEIMDALL_TEST_POSTGRES_BIND_ROOT` to an existing disk-backed directory if the
Docker data volume is constrained. The probe removes its database container.

## Deployment

After audit v5 activation, run the deployment CLI with
`--install-retirement-index --writers-drained --sql-directory` and the shared
session SQL directory. Drain application writers and maintenance jobs before
running the command. Index creation takes a table lock that blocks writes and
is limited to 20 seconds per statement. Budget disk space for index creation.
If the build exceeds the deadline, PostgreSQL rolls back the transaction.
After a lost commit response, rerun the exact command to validate the catalog.

```mermaid
flowchart LR
  Ready[Verify active audit v5] --> Lock[Acquire deployment and table locks]
  Lock --> Check[Validate exact v5 catalog]
  Check --> Build[Create index if absent]
  Build --> Verify[Verify the index and remaining catalog]
  Verify --> Commit[Commit physical schema change]
```

The maintenance command accepts the original catalog and the catalog with the
exact GIN index. A same-name index on another column or unrelated catalog drift
causes refusal. Both forms use the same v5 runtime protocol and migration
receipts, so the index requires no SDK upgrade. The updated migration command
recognizes both forms when retrying v5 activation. Use the updated command for
deployment operations once the index is installed.

Scope-heavy member ID collisions, large live index builds and index maintenance
under sustained write load still need qualification.

Railway PostgreSQL 17.11 run `c47c982752234b5db499af8bf62f3d96` installed
the index in a disposable application database, verified a repeat invocation,
and repeated v5 activation and restricted readiness. The SELECT-only role
could not install the index. The probe removed the database, role and upload
directory. Its JSON record is
`tests/integration/evidence/retirement-index-railway-2026-11-13.json`.

The populated local run `heimdall-capacity-backfill-247070192f2946fdb7eb019d03b682be`
refused a v4 database and an index with the expected name on the wrong column.
A proxy discarded the successful index commit response. The next invocation
verified the committed index. Backup and restore preserved the index, and both
SDKs passed their restored-state checks.
