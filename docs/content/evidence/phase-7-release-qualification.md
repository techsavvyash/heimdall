---
title: Phase 7 release qualification
---

Heimdall 0.1.0 is available in the [paired GitHub release](https://github.com/techsavvyash/heimdall/releases/tag/sdk/go/v0.1.0). The enterprise gate is recorded in [[phase-6-acceptance]]. This page tracks what the release checks establish for both SDKs.

| Gate | Verified evidence and outstanding checks |
| --- | --- |
| #41 and #42 Reference integrations | [[phase-4-reference-backends#phase-7-verification]] records invoice approval and ticket assignment. The [operator rehearsal](../reference-quickstart#rehearse-the-operator-handoff) ties the installed setup CLI to both reference backends. The patched reference deployments repeated the business-write outage checks on Railway. [[phase-7-tenant-matrix]] verifies both named workspaces and product-specific grants. |
| #43 Security and failure suite | [[phase-7-security-coverage]] maps all locked dry runs to test evidence and records remaining assertions. The `service-faults` CI job passed pinned PostgreSQL and OpenFGA fault coverage and offline key restore on the commit `51b4090` [pull request run](https://github.com/techsavvyash/heimdall/actions/runs/43557924136). |
| #49 Mixed SDK versions and rollback | [[phase-3-acceptance]] covers schema admission and migration. [[phase-7-model-rollout]] and [[phase-7-sdk-candidate]] cover staged model rollout and rollback. [[phase-7-mixed-persistence]] verifies same-schema rollback for the previous and tagged SDK builds, including persisted sessions, discovery, operation state and audit. |
| #50 Release artifacts | [[phase-7-sdk-candidate]] records the tagged Go module and checksummed ESM archive, candidate CI, live install and product verification, and versioned operating notes. The stable release uses GitHub release assets. |
| #45, #47 and #48 Installed candidate enterprise repeat | [[phase-7-sdk-candidate#scim-and-siem-on-the-installed-candidate]] records two-product session denial, revision-bound cleanup and tenant-scoped delivery from both installed reference audit jobs. Phase 6 covers the full tenant SSO and enterprise provider settings scenarios. |
| #44 Deployed candidate source enterprise repeat | [[phase-6-tenant-sso-gate#local-feedback-loop]] records local watch-mode tenant SSO and policy checks, followed by the successful Railway repeat of tenant switching, tested provider activation and strict-workspace cleanup from rc.2 source. |
| #55 Adapter safety | [[phase-7-security-coverage]] covers pinned fault tests. The [offline session and key restore](phase-3-key-rotation-restore) and [retained audit and managed-state restore](phase-3-retained-storage-restore) cases passed again in Phase 7. [[phase-7-coordinated-restore]] covers restoring PostgreSQL after known live identity, relationship and audit delivery changes. The [existing-workspace rehearsal](#Existing workspace catalogs) verified the session adapter and core source path. The [incomplete-history drill](phase-7-coordinated-restore#incomplete-post-backup-history) verifies the database fence blocks runtime and audit export while reconciliation is incomplete. [[phase-7-adapter-measurements]] records request SQL counts, candidate-scan cost and the measured mutation and reconciliation intervals. |


## Dependency scan

The production TypeScript package audit reported zero known vulnerabilities. The Go SDK and Support reference example passed `govulncheck` with Go 1.26.11, and the SDK race suite passed on that toolchain. The scan on Go 1.26.4 reported reachable standard-library advisories. The Go 1.24 module directive describes language compatibility. Railway built and verified the Support reference binary with Go 1.26.11. [Go release history](https://go.dev/doc/devel/release)

## Publish the paired SDKs

TypeScript is distributed as the package `@techsavvyash/heimdall-sdk` on GitHub Releases. Go resolves the nested module from the same `sdk/go/v<version>` tag. The two package versions and runtime markers match.

The paired SDK workflow runs the workspace and pinned-service checks, installs the tagged Go module in a clean consumer, and uploads the checked TypeScript archive with its SHA-256 manifest. Download the workflow artifact, verify the checksum, and run the reference journeys from that archive and the Go tag before publishing the GitHub release. Attach the same archive and manifest to the release.

```sh
gh release download sdk/go/v0.1.0 --repo techsavvyash/heimdall \
  --pattern techsavvyash-heimdall-sdk-0.1.0.tgz --pattern SHA256SUMS
shasum -a 256 -c SHA256SUMS
pnpm add ./techsavvyash-heimdall-sdk-0.1.0.tgz
go get github.com/techsavvyash/heimdall/sdk/go@v0.1.0
```

In TypeScript, import the runtime from `@techsavvyash/heimdall-sdk`, the PostgreSQL adapters from `@techsavvyash/heimdall-sdk/postgres`, and provisioning from `@techsavvyash/heimdall-sdk/provisioning`. Keep the downloaded tarball next to the project if its manifest refers to it with a file dependency.

The [stable tag build](https://github.com/techsavvyash/heimdall/actions/runs/43582400136) passed at `dea5f11`. The archive downloaded from the release verified as SHA-256 `19aade67b94d1e84b1582e92274bf01d978a736e3471e2536a1930a5f9df73f5`. Both the public archive and Go module passed clean consumer checks.

Run `heimdall-records-a5fb309d7c3e406cabbc6b1ba34910a0` installed the release archive and tagged Go module in independent reference consumers. Both passed setup replay, browser sessions, onboarding reconciliation, role enforcement, and audit outage denial. Each matched 48 authorization requests to terminal receipts and passed the `0.1.0` audit version check.

Railway run `8adbdf56-5731-495d-8c32-908b20e1f7e3` verified the tagged source in Ledger deployment `565015f8-8d00-4416-8e7f-a6f4428b55ee` and Support deployment `68a1d678-3a75-406c-b1dd-7e04378c2105`. Both passed tenant federation switching, tested provider activation and replayed strict-workspace cleanup. The release record is `tests/integration/evidence/phase-7-github-release-2026-11-14.json`.

### Stable build rehearsal

Commit `d24fe6e` on the [stable preparation branch](https://github.com/techsavvyash/heimdall/tree/codex/release-0.1.0) aligns the TypeScript package and both SDK runtime markers at `0.1.0`. The workspace suite and pinned-service fault run `heimdall-records-e94b481a42b145ae884878b5779e5ae9` passed. The local TypeScript archive passed clean-install verification and an npm publish dry run. Its SHA-256 was `88ed42a2f31320b49113964f7bf2cc3d62a407f86583a0b0cc5dfe053b2c8720`.

Run `heimdall-records-d10e68c52a034cc7b28be611b8752dfd` exercised both source-built products with disposable PostgreSQL and the Railway verification providers. Both completed the authorization and onboarding journeys, passed the business write and audit-outage checks, and matched 48 authorization requests to terminal receipts per product. The checked audit events reported `0.1.0`. This rehearsal used the package name from before the GitHub release distribution decision.


## Isolated consumer journeys

Run `heimdall-records-813b9518a438456f953b23beec527f45` used a fresh Ledger install of `heimdall-sdk-0.0.0.tgz` with strict 30-day dependency age enforcement and fetched Support's Go module at `v0.0.0-20261114045309-432c7e386493` from GitHub. The run record in `tests/integration/evidence/phase-7-clean-consumers-2026-11-14.json` contains the archive/module identities and results.

Both apps ran outside the repository workspace against disposable PostgreSQL and the Railway verification ZITADEL and OpenFGA. Both completed sign-in and logout, managed discovery and onboarding, replay and reconciliation of a confirmed but pending workspace, role assignment and scoped business writes. Both returned HTTP 503 with the business row unchanged when the decision audit insert failed. The harness checked the persisted onboarding and authorization audit chain. The pinned fault suite supplies the separate uncertain-write recovery proof. The quickstart migration commands also reached `managed-v5` on fresh PostgreSQL 17.11 in run `heimdall-quickstart-3218b57a61ed403394d4770623ae366b`.

## Operator rehearsal

Run `heimdall-records-f1003f7b4d5045848017a819a1d4d421` used the installed CLI from `heimdall-sdk-0.0.0.tgz`. It initialized an independent PostgreSQL setup journal and provisioned separate product audiences, clients, OpenFGA stores and models on the Railway verification providers. Four steps were confirmed per product. Repeating either plan returned the same runtime configuration and journal state.

Ledger took its generated config and client secret from that setup. Support took its own config and secret and used the downloaded Go module `v0.0.0-20261114062316-fb296691c911`. Both ran with restricted PostgreSQL logins against managed-v5 storage, consumed their separately registered record keys, and completed OIDC callbacks, discovery, onboarding, role changes, scoped business writes and logout. The startup environment received runtime configuration and secrets only. The verification harness supplies the business rows, initial Acme/Globex fixtures and ZITADEL Login API session. The enterprise jobs have separate [[phase-6-acceptance|live acceptance evidence]]. The run record is `tests/integration/evidence/phase-7-operator-quickstart-2026-11-14.json`.


## Patched Railway reference deployments

Run `62412952-458a-4dc9-9900-17e4f41407ed` verified Ledger deployment `45753248-288e-4ac2-a6d0-9f2eab4fb485` and Support deployment `f7ac0870-ab0a-494a-97f9-9b268548b2bf` from source `432c7e3`. The Support build log reported `/support: go1.26.11` for image `sha256:673c1d7ce822c37d2cc2793059918ec55bc02676bbac4bfae66428d53f108fc3`. Both deployments reported this run in their HTTPS readiness responses. Both returned HTTP 503 with no row change when audit insertion was denied, rejected pending workspaces with HTTP 403 and returned HTTP 404 for a foreign resource ID. Allowed requests and idempotent repeats returned HTTP 204. The probe checked business row ownership after the writes and the denial of logged-out cookies.

## Existing workspace catalogs

Run `heimdall-records-12cd9128257e41709727c21e4411ca6a` exercised the installed TypeScript archive and downloaded Support Go SDK revision against Railway verification ZITADEL and OpenFGA. The disposable PostgreSQL application database had session, identity and audit storage and product workspace catalogs. The managed workspace schema was absent.

Both products discovered Acme, denied access to Globex, and denied an inactive workspace whose membership remained present in OpenFGA. Changing Acme to inactive in the product catalog also removed it from discovery and denied direct reads while its OpenFGA membership remained. Both sessions survived a restart and failed after logout. The permitted invoice approval and ticket assignment returned HTTP 204. Globex writes returned HTTP 403 and left the business rows unchanged. PostgreSQL held a matching request and terminal receipt for each allow and deny. The managed workspace creation route was unavailable in both compositions.

See the [[../reference-quickstart#use-an-existing-workspace-catalog|example wiring]] and `tests/integration/evidence/phase-7-existing-workspaces-2026-11-14.json`.
