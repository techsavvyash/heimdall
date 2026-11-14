---
title: Phase 7 release qualification
---

Release qualification is in progress. The enterprise gate is recorded in [[phase-6-acceptance]]. This page tracks what the release checks establish for both SDKs.

| Gate | Verified evidence and outstanding checks |
| --- | --- |
| #41 and #42 Reference integrations | [[phase-4-reference-backends#phase-7-verification]] records invoice approval and ticket assignment. The [operator rehearsal](../reference-quickstart#rehearse-the-operator-handoff) ties the installed setup CLI to both reference backends. The patched reference deployments repeated the business-write outage checks on Railway. [[phase-7-tenant-matrix]] verifies both named workspaces and product-specific grants. |
| #43 Security and failure suite | [[phase-7-security-coverage]] maps all locked dry runs to test evidence and records remaining assertions. The `service-faults` CI job passed pinned PostgreSQL and OpenFGA fault coverage and offline key restore on the commit `51b4090` [pull request run](https://github.com/techsavvyash/heimdall/actions/runs/43557924136). |
| #49 Mixed SDK versions and rollback | [[phase-3-acceptance]] covers schema admission and migration. [[phase-7-model-rollout]] and [[phase-7-sdk-candidate]] cover staged model rollout and rollback. [[phase-7-mixed-persistence]] verifies same-schema rollback for the previous and tagged SDK builds, including persisted sessions, discovery, operation state and audit. |
| #50 Release artifacts | [[phase-7-sdk-candidate]] records the tagged Go module and checksummed ESM archive, candidate CI, live install and product verification, and versioned operating notes. Stable registry publication and the remaining release gates are open. |
| #45, #47 and #48 Installed candidate enterprise repeat | [[phase-7-sdk-candidate#scim-and-siem-on-the-installed-candidate]] records two-product session denial, revision-bound cleanup and tenant-scoped delivery from both installed reference audit jobs. Phase 6 covers the full tenant SSO and enterprise provider settings scenarios. |
| #44 Deployed candidate source enterprise repeat | [[phase-6-tenant-sso-gate#local-feedback-loop]] records local watch-mode tenant SSO and policy checks, followed by the successful Railway repeat of tenant switching, tested provider activation and strict-workspace cleanup from rc.2 source. |
| #55 Adapter safety | [[phase-7-security-coverage]] covers pinned fault tests. The [offline session and key restore](phase-3-key-rotation-restore) and [retained audit and managed-state restore](phase-3-retained-storage-restore) cases passed again in Phase 7. [[phase-7-coordinated-restore]] covers restoring PostgreSQL after known live identity, relationship and audit delivery changes. The [existing-workspace rehearsal](#Existing workspace catalogs) verified the session adapter and core source path. The [incomplete-history drill](phase-7-coordinated-restore#incomplete-post-backup-history) verifies the database fence blocks runtime and audit export while reconciliation is incomplete. [[phase-7-adapter-measurements]] records request SQL counts, candidate-scan cost and the measured mutation and reconciliation intervals. |


## Dependency scan

The production TypeScript package audit reported zero known vulnerabilities. The Go SDK and Support reference example passed `govulncheck` with Go 1.26.11, and the SDK race suite passed on that toolchain. The scan on Go 1.26.4 reported reachable standard-library advisories. The Go 1.24 module directive describes language compatibility. Railway built and verified the Support reference binary with Go 1.26.11. [Go release history](https://go.dev/doc/devel/release)

## Publish the paired SDKs

The npm maintainer needs publish access to `@heimdall/sdk`. Confirm the scope in npm and authenticate in the maintainer's environment. The current verification session returns HTTP 401 from `npm whoami --registry=https://registry.npmjs.org`. A public package lookup cannot verify scope ownership. [npm organization package publishing](https://docs.npmjs.com/creating-and-publishing-an-organization-scoped-package/)

Use the approved stable version in the TypeScript manifest and both runtime version constants. Remove the TypeScript manifest's `private` flag in the stable release commit. Run the workspace and pinned-service checks on that commit, and use the same commit for the nested Go tag `sdk/go/v<version>`. Run the reference journeys against the stable build and record the artifact checksums and Railway deployment IDs.

From a clean checkout of the approved release commit, build the TypeScript archive once and check the exact file that will go to npm.

```sh
pnpm install --frozen-lockfile
pnpm run check
release_dir="$(mktemp -d)"
version="$(node -p 'require("./sdk/typescript/package.json").version')"
archive="$release_dir/heimdall-sdk-$version.tgz"
pnpm --filter @heimdall/sdk pack --pack-destination "$release_dir"
pnpm run check:package "$archive"
shasum -a 256 "$archive"
npm publish "$archive" --registry=https://registry.npmjs.org --access public --dry-run
```

During the authorized release, publish that archive and push the matching Go tag. If either operation fails, verify which artifact is already available and resume the other operation with the same archive and release commit. Verify npm's `dist.integrity` against the SHA-512 of the local archive. Verify both installs from fresh consumers and check that the Go tag resolves to the same release commit before announcing the pair. [npm publish](https://docs.npmjs.com/commands/npm-publish/)


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
