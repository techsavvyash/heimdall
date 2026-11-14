---
title: Phase 7 release qualification
---

Release qualification is in progress. The enterprise gate is recorded in [[phase-6-acceptance]]. This page tracks what the release checks establish for both SDKs.

| Gate | Verified evidence and outstanding checks |
| --- | --- |
| #41 and #42 Reference integrations | [[phase-4-reference-backends#phase-7-verification]] records invoice approval and ticket assignment. The [[../reference-quickstart#rehearse-the-operator-handoff|operator rehearsal]] ties the installed setup CLI to both reference backends. The patched reference deployments repeated the business-write outage checks on Railway. [[phase-7-tenant-matrix]] verifies both named workspaces and product-specific grants. |
| #43 Security and failure suite | [[phase-7-security-coverage]] maps all locked dry runs to test evidence and records remaining assertions. The `service-faults` CI job passed pinned PostgreSQL and OpenFGA fault coverage and offline key restore on the commit `51b4090` [pull request run](https://github.com/techsavvyash/heimdall/actions/runs/43557924136). |
| #49 Mixed SDK versions and rollback | [[phase-3-acceptance]] covers storage compatibility. [[phase-7-model-rollout]] verifies staged model rollout and rollback with the current TS and Go implementations for both products. The provisioning inspection verifies both published pins and reports authorization drift in both SDKs. The mixed-build rehearsal verifies two installed SDK revisions for both products on Railway. Tagged artifacts and persistence compatibility across those artifacts remain release checks. |
| #50 Release artifacts | The packed ESM TypeScript SDK installs in an isolated project with the 30-day age rule. `sh scripts/check-go-consumer.sh 3becec4ec0ef2ac9fb209d765611ff73bef5d670` resolved the GitHub module as `v0.0.0-20261114040723-3becec4ec0ef` and compiled and ran a clean consumer importing core, PostgreSQL and provisioning. The isolated reference journeys below passed. Tagged candidates, publishing and release notes still need verification. |
| #55 Adapter safety | [[phase-7-security-coverage]] covers pinned fault tests. The [[phase-3-key-rotation-restore|offline session and key restore]] and [[phase-3-retained-storage-restore|retained audit and managed-state restore]] cases passed again in Phase 7. [[phase-7-coordinated-restore]] covers restoring PostgreSQL after known live identity, relationship and audit delivery changes. Unknown post-backup history and mixed release artifacts still need evidence. |


## Dependency scan

The production TypeScript package audit reported zero known vulnerabilities. The Go SDK and Support reference example passed `govulncheck` with Go 1.26.11, and the SDK race suite passed on that toolchain. The scan on Go 1.26.4 reported reachable standard-library advisories. The Go 1.24 module directive describes language compatibility. Railway built and verified the Support reference binary with Go 1.26.11. [Go release history](https://go.dev/doc/devel/release)


## Isolated consumer journeys

Run `heimdall-records-813b9518a438456f953b23beec527f45` used a fresh Ledger install of `heimdall-sdk-0.0.0.tgz` with strict 30-day dependency age enforcement and fetched Support's Go module at `v0.0.0-20261114045309-432c7e386493` from GitHub. The run record in `tests/integration/evidence/phase-7-clean-consumers-2026-11-14.json` contains the archive/module identities and results.

Both apps ran outside the repository workspace against disposable PostgreSQL and the Railway verification ZITADEL and OpenFGA. Both completed sign-in and logout, managed discovery and onboarding, replay and reconciliation of a confirmed but pending workspace, role assignment and scoped business writes. Both returned HTTP 503 with the business row unchanged when the decision audit insert failed. The harness checked the persisted onboarding and authorization audit chain. The pinned fault suite supplies the separate uncertain-write recovery proof. The quickstart migration commands also reached `managed-v5` on fresh PostgreSQL 17.11 in run `heimdall-quickstart-3218b57a61ed403394d4770623ae366b`.

## Operator rehearsal

Run `heimdall-records-f1003f7b4d5045848017a819a1d4d421` used the installed CLI from `heimdall-sdk-0.0.0.tgz`. It initialized an independent PostgreSQL setup journal and provisioned separate product audiences, clients, OpenFGA stores and models on the Railway verification providers. Four steps were confirmed per product. Repeating either plan returned the same runtime configuration and journal state.

Ledger took its generated config and client secret from that setup. Support took its own config and secret and used the downloaded Go module `v0.0.0-20261114062316-fb296691c911`. Both ran with restricted PostgreSQL logins against managed-v5 storage, consumed their separately registered record keys, and completed OIDC callbacks, discovery, onboarding, role changes, scoped business writes and logout. The startup environment received runtime configuration and secrets only. The verification harness supplies the business rows, initial Acme/Globex fixtures and ZITADEL Login API session. The enterprise jobs have separate [[phase-6-acceptance|live acceptance evidence]]. The run record is `tests/integration/evidence/phase-7-operator-quickstart-2026-11-14.json`.


## Patched Railway reference deployments

Run `62412952-458a-4dc9-9900-17e4f41407ed` verified Ledger deployment `45753248-288e-4ac2-a6d0-9f2eab4fb485` and Support deployment `f7ac0870-ab0a-494a-97f9-9b268548b2bf` from source `432c7e3`. The Support build log reported `/support: go1.26.11` for image `sha256:673c1d7ce822c37d2cc2793059918ec55bc02676bbac4bfae66428d53f108fc3`. Both deployments reported this run in their HTTPS readiness responses. Both returned HTTP 503 with no row change when audit insertion was denied, rejected pending workspaces with HTTP 403 and returned HTTP 404 for a foreign resource ID. Allowed requests and idempotent repeats returned HTTP 204. The probe checked business row ownership after the writes and the denial of logged-out cookies.
