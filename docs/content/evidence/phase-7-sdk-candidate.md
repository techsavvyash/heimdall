---
title: Heimdall SDK release candidate 0.1.0-rc.1
---

The first paired candidate is for release qualification. TypeScript uses Node 24 and ESM. Go exposes a nested module with Go 1.24 language compatibility and uses Go 1.26.11 for qualification. The pinned verification services are PostgreSQL 17.11, OpenFGA 1.22.0 and ZITADEL API and Login 4.19.4. Image digests are recorded in `deploy/verification/versions.json`.

## Install the candidate

Download the TypeScript archive and its SHA-256 manifest from the [tagged GitHub Actions run](https://github.com/techsavvyash/heimdall/actions/runs/43569261855#artifacts). Verify the archive hash, then use the local archive as the dependency. Go resolves the same Git tag using the nested-module path.

```sh
shasum -a 256 -c SHA256SUMS
pnpm add ./heimdall-sdk-0.1.0-rc.1.tgz
go get github.com/techsavvyash/heimdall/sdk/go@v0.1.0-rc.1
```

The candidate workflow checks the version pair, frozen dependencies, shared workspace suite, pinned-service fault cases and a fresh Go module consumer. It stores the ESM tarball with the checksum in GitHub Actions. The TypeScript package is private in this build and distributed through the archive. The 30-day dependency age rule remains enabled for clean installations.

The tag resolves to commit `a0e1168e1954867bde092793ba0e9a9258401856`. The downloaded TypeScript archive verified as SHA-256 `ea1e86ef500d644feb2b55d7baa476092ea707e4f9841f86fc219c48eea267e7`.

## Live qualification

Run `dfbee61a-628e-4da8-a30b-482132bc7a13` installed that exact archive and downloaded the tagged Go module. It compared both SDKs with the previous builds at `4eeddb7` against the pinned Railway providers. Both products passed previous and candidate model overlap, mismatched pin rejection, new role enforcement and rollback after removing the candidate-only role grant. This run covers model compatibility. Persistence compatibility and recovery across releases have additional gates in [[phase-7-release-qualification]].

Run `heimdall-records-7000c045123c458eb73b3b202edc8ed4` used the same artifacts for both reference products. It provisioned both products with the installed TypeScript CLI, verified unchanged setup replay and ran both backends on managed-v5 PostgreSQL. Both passed sign-in, workspace discovery and onboarding, role enforcement, business writes and protected-work denial during the injected audit outage. See `tests/integration/evidence/phase-7-sdk-candidate-2026-11-14.json` for both run records.

## Scope and operating requirements

- Both SDKs provide authentication, product roles and workspace authorization, browser sessions, workspace discovery and managed workspace operations. Use the [[../reference-quickstart|Ledger and Support quickstart]] for the application routes, storage, keys and product policies.
- Tenant SAML and OIDC, SCIM user lifecycle, and durable SIEM delivery are first-release requirements. See [[phase-6-acceptance]] for live provider evidence and the job configuration. Those results describe the tested deployments.
- Migration and rollback require the supported database schema, verified model and role tuple compatibility, and a writer drain where specified. See [[../design/operations|operations]] and [[phase-7-coordinated-restore|restore qualification]].
- Required audit persistence gates protected work. Uncertain dispatched operations keep their workspace gate in place pending verified quiescence and reconciliation. Recovery with incomplete post-backup history remains part of release qualification.
- The package version and Git tag identify the artifacts. Runtime audit envelopes in this candidate still use the development version value `0.0.0`. Updating that value requires preserving deterministic retries and historical event digests across versions.
