---
title: Adapter costs in Ledger and Support
---

The reference journey measures ordinary requests against pinned PostgreSQL 17.11 and the Railway verification ZITADEL and OpenFGA providers. Each product uses one local backend. Ledger runs TypeScript and Support runs Go. Measurements use the same committed audit policy as the reference journey.

## Method

- PostgreSQL `pg_stat_statements` counts top-level SQL statements beginning with `SELECT` during each request, excluding the measurement query. These counts include product SQL, session checks, managed reads, audit work and advisory-lock statements where they use `SELECT`. Queries inside write statements, PostgreSQL function bodies and triggers are outside the count.
- Candidate scans count the statements reading the managed candidate and workspace join. The sparse run seeds 20 additional active workspace candidates with no OpenFGA membership. Both discovery requests must return only Acme, and the probe requires more scan statements for the sparse request.
- A fixture trigger timestamps the transitions into and out of the mutation gate during a confirmed membership grant. The difference is measured within PostgreSQL between the transition statements.
- The recovery sample starts when the harness observes the committed pending state. It ends on the first successful protected read after reconciliation. The interval includes the deliberately denied write and request overhead. The reconcile request has its own timing.

## Observed costs

Run `heimdall-records-1dc9a847f22348dbb47ad25e5a621ecb` produced these single-request samples.

| Request | Ledger SELECTs | Support SELECTs | Ledger elapsed | Support elapsed |
| --- | ---: | ---: | ---: | ---: |
| Protected business read | 57 | 62 | 488 ms | 509 ms |
| Discovery with two candidates | 106 | 114 | 612 ms | 596 ms |
| Discovery with 22 candidates | 708 | 756 | 3627 ms | 3925 ms |
| Create workspace | 99 | 107 | 1312 ms | 1115 ms |
| Add member | 99 | 107 | 1145 ms | 1131 ms |
| Reconcile confirmed operation | 29 | 32 | 339 ms | 333 ms |

Discovery ran one candidate scan with two candidates and two scans with 22 candidates in each SDK. Both returned one accessible workspace. Adding 20 unauthorized candidates added 602 top-level SELECTs in Ledger and 642 in Support. The total includes the audit and freshness checks around per-candidate evaluation. This is an end-to-end cost sample with providers across the network. It does not establish throughput, tail latency or a supported candidate limit.

The membership grant's gate transition interval was 332 ms for Ledger and 288 ms for Support. After the pending state was observed, the first allowed read followed in 1,471 ms for Ledger and 1,287 ms for Support. Operator quiescence and uncertain outcomes have a separate [[phase-2-railway-recovery|recovery procedure]] and depend on external evidence. The run record is `tests/integration/evidence/phase-7-adapter-measurements-2026-11-14.json`.

## Consumer wiring

The clean operator rehearsal uses one plan and manifest per product, one independent setup journal, application database migrations, fresh record-key registration, and the product server launch. The existing-workspace composition uses the session, identity and audit storage and a scan of the product's active catalog. Managed workspaces additionally use the SDK lifecycle gate and require operator scheduling of reconciliation. Both compositions keep business queries in the product. The quickstart spells out the required credentials, route mounting, product policy and scheduled jobs. [[../reference-quickstart|Reference quickstart]]

## Reproduce

Use the verification provider credentials from the [[../reference-quickstart#verify-package-integration|reference quickstart]].

```sh
HEIMDALL_PROBE_REFERENCE_PRODUCTS=1 HEIMDALL_PROBE_ADAPTER_COST=1 \
HEIMDALL_PROBE_BUSINESS_WRITE=1 python3 tests/integration/postgres-records.py
```
