---
title: Phase 7 release qualification
---

Release qualification is in progress. The enterprise gate is recorded in [[phase-6-acceptance]]. This page tracks what the release checks establish for both SDKs.

| Gate | Verified evidence and outstanding checks |
| --- | --- |
| #41 and #42 Reference integrations | [[phase-4-reference-backends#phase-7-verification]] records invoice approval and ticket assignment. The [[../reference-quickstart|two-product quickstart]] documents operator setup and runtime wiring. Isolated consumers exercised both applications, including business-write outage handling. Explicit Acme/Globex role coverage and deployed reference acceptance are tracked below. |
| #43 Security and failure suite | [[phase-7-security-coverage]] maps all locked dry runs to test evidence and records remaining assertions. The `service-faults` CI job passed pinned PostgreSQL and OpenFGA fault coverage and offline key restore on the commit `51b4090` [pull request run](https://github.com/techsavvyash/heimdall/actions/runs/43557924136). |
| #49 Mixed SDK versions and rollback | [[phase-3-acceptance]] covers storage compatibility. [[phase-7-model-rollout]] verifies staged model rollout and rollback with the current TS and Go implementations for both products. Mixed release artifacts and provisioning compatibility reporting still need evidence. |
| #50 Release artifacts | The packed ESM TypeScript SDK installs in an isolated project with the 30-day age rule. `sh scripts/check-go-consumer.sh 3becec4ec0ef2ac9fb209d765611ff73bef5d670` resolved the GitHub module as `v0.0.0-20261114040723-3becec4ec0ef` and compiled and ran a clean consumer importing core, PostgreSQL and provisioning. The isolated reference journeys below passed. Tagged candidates, publishing and release notes still need verification. |
| #55 Adapter safety | [[phase-7-security-coverage]] covers pinned fault tests. The [[phase-3-key-rotation-restore|offline session and key restore]] and [[phase-3-retained-storage-restore|retained audit and managed-state restore]] cases passed again in Phase 7. Coordinated restore against provider changes, mixed release artifacts and clean consumer wiring still need proof. |


## Dependency scan

The production TypeScript package audit reported zero known vulnerabilities. The Go SDK and Support reference example passed `govulncheck` with Go 1.26.11, and the SDK race suite passed on that toolchain. The scan on Go 1.26.4 reported reachable standard-library advisories. The Go 1.24 module directive describes language compatibility. Release deployment still needs a supported patched builder and verification of its deployed binary. [Go release history](https://go.dev/doc/devel/release)


## Isolated consumer journeys

Run `heimdall-records-813b9518a438456f953b23beec527f45` used a fresh Ledger install of `heimdall-sdk-0.0.0.tgz` with strict 30-day dependency age enforcement and fetched Support's Go module at `v0.0.0-20261114045309-432c7e386493` from GitHub. The run record in `tests/integration/evidence/phase-7-clean-consumers-2026-11-14.json` contains the archive/module identities and results.

Both apps ran outside the repository workspace against disposable PostgreSQL and the Railway verification ZITADEL and OpenFGA. Both completed sign-in and logout, managed discovery and onboarding, replay and reconciliation of a confirmed but pending workspace, role assignment and scoped business writes. Both returned HTTP 503 with the business row unchanged when the decision audit insert failed. The harness checked the persisted onboarding and authorization audit chain. The pinned fault suite supplies the separate uncertain-write recovery proof. The sample apps used the probe's provisioned clients and storage, so this run covers the consumer runtime and package path. The full operator sequence in the quickstart still needs a clean deployment rehearsal.
