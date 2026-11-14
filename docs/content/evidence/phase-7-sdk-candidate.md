---
title: Heimdall SDK release candidates
---

The paired candidates are for release qualification. TypeScript uses Node 24 and ESM. Go exposes a nested module with Go 1.24 language compatibility and uses Go 1.26.11 for qualification. The pinned verification services are PostgreSQL 17.11, OpenFGA 1.22.0 and ZITADEL API and Login 4.19.4. Image digests are recorded in `deploy/verification/versions.json`.

## Install the candidate

Download the TypeScript archive and its SHA-256 manifest from the [rc.2 GitHub Actions run](https://github.com/techsavvyash/heimdall/actions/runs/43573001067#artifacts). Verify the archive hash, then use the local archive as the dependency. Go resolves the same Git tag using the nested-module path.

```sh
shasum -a 256 -c SHA256SUMS
pnpm add ./heimdall-sdk-0.1.0-rc.2.tgz
go get github.com/techsavvyash/heimdall/sdk/go@v0.1.0-rc.2
```

The candidate workflow checks the version pair, frozen dependencies, shared workspace suite, pinned-service fault cases and a fresh Go module consumer. It stores the ESM tarball with the checksum in GitHub Actions. The TypeScript package is private in this build and distributed through the archive. The 30-day dependency age rule remains enabled for clean installations.

The rc.2 tag resolves to commit `1458a7f8760903401fbbc5b9577520e89e28bcd9`.

## Live qualification of rc.1

The [rc.1 GitHub Actions run](https://github.com/techsavvyash/heimdall/actions/runs/43569261855#artifacts) built commit `a0e1168e1954867bde092793ba0e9a9258401856`. The downloaded rc.1 TypeScript archive verified as SHA-256 `ea1e86ef500d644feb2b55d7baa476092ea707e4f9841f86fc219c48eea267e7`.

Run `dfbee61a-628e-4da8-a30b-482132bc7a13` installed that exact archive and downloaded the tagged Go module. It compared both SDKs with the previous builds at `4eeddb7` against the pinned Railway providers. Both products passed previous and candidate model overlap, mismatched pin rejection, new role enforcement and rollback after removing the candidate-only role grant. The same pair underwent a [[phase-7-mixed-persistence|persistence rehearsal]].

Run `heimdall-records-7000c045123c458eb73b3b202edc8ed4` used the same artifacts for both reference products. It provisioned both products with the installed TypeScript CLI, verified unchanged setup replay and ran both backends on managed-v5 PostgreSQL. Both passed sign-in, workspace discovery and onboarding, role enforcement, business writes and protected-work denial during the injected audit outage. See `tests/integration/evidence/phase-7-sdk-candidate-2026-11-14.json` for both run records.

## Scope and operating requirements

- Both SDKs provide authentication, product roles and workspace authorization, browser sessions, workspace discovery and managed workspace operations. Use the [[../reference-quickstart|Ledger and Support quickstart]] for the application routes, storage, keys and product policies.
- Tenant SAML and OIDC, SCIM user lifecycle, and durable SIEM delivery are first-release requirements. See [[phase-6-acceptance]] for live provider evidence and the job configuration. Those results describe the tested deployments.
- Migration and rollback require the supported database schema, verified model and role tuple compatibility, and a writer drain where specified. See [[../design/operations|operations]] and [[phase-7-coordinated-restore|restore qualification]].
- Required audit persistence gates protected work. Uncertain dispatched operations keep their workspace gate in place pending verified quiescence and reconciliation. Recovery with incomplete post-backup history remains part of release qualification.

## Audit attribution in rc.2

The second candidate adds build attribution for authentication, authorization, managed operations and export events. Both SDK build constants must match the package version and Go candidate tag. Existing payloads retain their original digest. The compatibility-sensitive paths that reconstruct deterministic event IDs retain the original envelope field. See the [[../design/audit-contract|audit contract]].

The tagged build passed the workspace checks, pinned PostgreSQL and OpenFGA fault suite, and clean Go module import. The downloaded TypeScript archive verified as SHA-256 `01093773609dea5e64ddb36ddd4ddf3113f4c964c93c7ebd0b3a0b33d6e7565b`.

Live run `heimdall-records-8923ebc1df8547e18a5e44dff03065d2` installed that archive and the tagged Go module in independent consumers. The installed TypeScript CLI confirmed four setup steps per product, with unchanged replay. Both backends used managed-v5 PostgreSQL and the pinned Railway ZITADEL and OpenFGA providers. Both passed browser sign-in and logout, discovery, onboarding replay and reconciliation, scoped business writes and audit outage denial. The persisted authentication and authorization events, onboarding steps, and membership and ownership administration events passed the rc.2 version check. Each product matched 48 authorization requests with their terminal receipts. See `tests/integration/evidence/phase-7-sdk-candidate-rc2-2026-11-14.json`.
