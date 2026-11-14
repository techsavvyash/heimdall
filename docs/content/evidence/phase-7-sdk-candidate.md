---
title: Heimdall SDK release candidate 0.1.0-rc.1
---

The first paired candidate is for release qualification. TypeScript uses Node 24 and ESM. Go exposes a nested module with Go 1.24 language compatibility and uses Go 1.26.11 for qualification. The pinned verification services are PostgreSQL 17.11, OpenFGA 1.22.0 and ZITADEL API and Login 4.19.4. Image digests are recorded in `deploy/verification/versions.json`.

## Install the candidate

Download the TypeScript archive and its SHA-256 manifest from the GitHub Actions run associated with `sdk/go/v0.1.0-rc.1`. Verify the archive hash, then use the local archive as the dependency. Go resolves the same Git tag using the nested-module path.

```sh
shasum -a 256 -c SHA256SUMS
pnpm add ./heimdall-sdk-0.1.0-rc.1.tgz
go get github.com/techsavvyash/heimdall/sdk/go@v0.1.0-rc.1
```

The candidate workflow checks the version pair, frozen dependencies, shared workspace suite, pinned-service fault cases and a fresh Go module consumer. It stores the ESM tarball with the checksum in GitHub Actions. The TypeScript package is private in this build and distributed through the archive. The 30-day dependency age rule remains enabled for clean installations.

## Scope and operating requirements

- Both SDKs provide authentication, product roles and workspace authorization, browser sessions, workspace discovery and managed workspace operations. Use the [[../reference-quickstart|Ledger and Support quickstart]] for the application routes, storage, keys and product policies.
- Tenant SAML and OIDC, SCIM user lifecycle, and durable SIEM delivery are first-release requirements. See [[phase-6-acceptance]] for live provider evidence and the job configuration. Those results describe the tested deployments.
- Migration and rollback require the supported database schema, verified model and role tuple compatibility, and a writer drain where specified. See [[../design/operations|operations]] and [[phase-7-coordinated-restore|restore qualification]].
- Required audit persistence gates protected work. Uncertain dispatched operations keep their workspace gate in place pending verified quiescence and reconciliation. Recovery with incomplete post-backup history remains part of release qualification.
- The package version and Git tag identify the artifacts. Runtime audit envelopes in this candidate still use the development version value `0.0.0`. Updating that value requires preserving deterministic retries and historical event digests across versions.
