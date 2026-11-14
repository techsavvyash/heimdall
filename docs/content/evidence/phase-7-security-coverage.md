---
title: Phase 7 security and fault coverage
---

The GitHub Actions `workspace` job runs deterministic TypeScript and Go tests, package checks, and Quartz validation. The `service-faults` job runs the existing PostgreSQL and OpenFGA fault harnesses on isolated, digest-pinned PostgreSQL 17.11 and OpenFGA 1.22.0. Both SDKs share the test fixtures. The job uses disposable containers and generated credentials. Railway evidence separately exercises the deployed products and enterprise identity provider.

| First-release dry run | Evidence and remaining checks |
| --- | --- |
| Support sign-in after Ledger | The [[phase-2-oidc]] probe asserts each token has its intended project audience and excludes the other audience. [[phase-4-reference-backends]] verifies independent product sessions and scoped read decisions. [[phase-7-tenant-matrix]] records the same user signing into Support after receiving Ledger access and verifies denial before an explicit Support grant. |
| Acme route with a Globex resource ID | [[phase-4-reference-backends#phase-7-verification]] records scoped HTTP 404 in both business mutation routes. |
| Confirmed membership removal with stale role tuples | [[phase-4-reference-backends]] verifies denial with a stale owner tuple and no membership on Railway. [[phase-5-membership-administration]] verifies removal and resulting checks on pinned services. |
| Disabled identity and stale proof with provider outage | [[phase-3-identity-freshness]] verifies the shared freshness and provider failure behavior. [[phase-6-scim-deactivation]] verifies that a real directory disable blocks both product sessions. |
| Authorization began before revocation | [[operations#decision-consistency]] documents the in-flight boundary. The ordered product write, revocation, and outcome receipt still need one deterministic test. |
| Tenant administrator supplies an unknown role | The shared membership administration test submits an unconfigured role through both public SDKs and asserts `invalid_argument` with no OpenFGA tuple write. The tests run in the pinned administration pass. |
| Ownership transfer loses a remote response | [[phase-5-ownership-transfer]] records both fault boundaries, quarantine, and recovery in both SDKs. The `service-faults` job enables the pinned provider repeat and separately exercises a stopped OpenFGA writer, credential replacement and verified onboarding recovery. |
| Password session attempts entry to a tenant that requires SAML | [[phase-6-tenant-sso-gate]] records the deployed policy denial and compliant sign-in in both products. |
| Directory disables a user | [[phase-6-scim-deactivation]] and [[phase-6-directory-job]] record two-product cleanup, tenant and directory scope, and recovery across scheduled runs. |
| SIEM unavailable | [[phase-6-audit-delivery]] verifies a sink restart and lost receipts. The `service-faults` job exercises the durable delivery and acknowledgement boundaries in the PostgreSQL outbox. |
| Local audit storage unavailable | [[phase-3-audited-decisions]] verifies failure at request and terminal receipt boundaries, including commit-response loss. The `service-faults` job exercises this against PostgreSQL. [[phase-4-reference-backends#phase-7-verification]] records both product routes returning HTTP 503 with unchanged business rows in a disposable deployment connected to Railway ZITADEL and OpenFGA. The deployed Railway products repeated the outage check with unchanged rows. See [[phase-7-release-qualification#patched-railway-reference-deployments]]. |

Run the pinned service-fault job locally from the repository root after installing workspace dependencies. It needs Node, pnpm, Go and a container runtime with Docker Compose. Port 8080 must be available for the isolated recovery deployment.

```sh
HEIMDALL_PROBE_PINNED_ADMIN=1 HEIMDALL_PROBE_SDK_RECOVERY=1 python3 tests/integration/postgres-records.py
```

See [[phase-7-release-qualification]] for the release gate status.
