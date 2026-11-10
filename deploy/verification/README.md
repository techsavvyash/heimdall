# Phase 2 service verification

This is a capability test environment, not production infrastructure. The current
slice deploys OpenFGA 1.22.0 and PostgreSQL 17.11 using pinned multi-platform image
digests. ZITADEL API and upstream Login UI 4.19.4 are deployed with preliminary checks; see [ZITADEL setup](zitadel.md).
No service version is yet certified for the full Heimdall release.

## Local reproduction

Use Docker Compose v2 and Node 24. Generate separate random hex secrets (for example,
32 bytes each with openssl rand -hex 32), supplying them through environment variables:
HEIMDALL_PROBE_POSTGRES_PASSWORD and HEIMDALL_PROBE_OPENFGA_KEY. Do not commit secrets.
The database URI uses the hex password without additional URL escaping.

    docker compose -f deploy/verification/compose.yaml up -d --wait postgres
    docker compose -f deploy/verification/compose.yaml run --rm migrate
    docker compose -f deploy/verification/compose.yaml up -d openfga
    export HEIMDALL_PROBE_OPENFGA_URL=http://127.0.0.1:8080
    pnpm run probe:openfga

Wait for GET /healthz to return 200 before probing. Each request has a 15-second
deadline; the probe does not retry mutations. It creates uniquely named stores,
checks both products, and deletes only the stores it created in finally. If a
create response is lost or the process is killed, inspect stores bearing its
heimdall-probe prefix and remove only that run's resources after verification.
A cleanup failure fails the probe. Never run against an unrelated production stack.

The probe verifies missing/invalid authentication, two stores/four workspace objects,
membership-gated checks, listing and pagination, immediate post-revocation checks
with HIGHER_CONSISTENCY, and the actual service-credential boundary. It also writes 1,001 direct memberships to verify the pinned 1,000-result
ListObjects cap, exhaustive stable Read pagination and 100-tuple write limit.
It is not a load benchmark, concurrent scan/recovery proof or test of the future SDK runtime.

    docker compose -f deploy/verification/compose.yaml down

Only for deliberate disposal of this test database, append --volumes to down.
The tracked Compose setup and complete OpenFGA probe passed from a fresh isolated
project on November 11, 2026; Railway and local evidence are recorded under
tests/integration/evidence/openfga-{limits,local}-2026-11-11.json.
This verifies OpenFGA/PostgreSQL reproduction, not local enterprise-stack setup.

## Railway reproduction

Use a dedicated verification environment in the Heimdall project. Create a private
PostgreSQL service from the same pinned image, with a volume at
/var/lib/postgresql/data and PGDATA=/var/lib/postgresql/data/pgdata. Set POSTGRES_USER,
POSTGRES_DB and generated POSTGRES_PASSWORD as above. Do not expose its TCP port.

Create OpenFGA from the same pinned image with the Compose variables, using the
PostgreSQL Railway private hostname. Configure startCommand=/openfga run,
preDeployCommand=["/openfga migrate"], pre-deploy timeout 120 seconds, and HTTP
healthcheck /healthz. Publish only port 8080 through Railway HTTPS after configuring
authentication. PostgreSQL traffic currently uses sslmode=disable on the isolated
platform network; this is explicitly a test configuration, not verified production
transport/durability policy. The test database role owns the OpenFGA database.

The observed CLI environment edit command did not apply settings. Use the documented
serviceInstanceUpdate mutation, inspect the resulting fields, and trigger
serviceInstanceDeployV2 to deploy current configuration. Redeploying a previous
image deployment can retain its old start configuration; verify running state and
health, not merely a SUCCESS status on an exited help command.

Sources:
- https://github.com/openfga/openfga/releases/tag/v1.22.0
- https://www.postgresql.org/docs/17/release-17-11.html
- https://openfga.dev/docs/getting-started/setup-openfga/configure-openfga
- https://docs.railway.com/integrations/api/manage-services

## Customer IdP fixture

[Enterprise IdP setup](enterprise-idp.md) describes the test-only upstream Keycloak
service used to exercise customer federation. It is not a Heimdall runtime dependency.
Run `pnpm run probe:federation` for upstream OIDC or set
HEIMDALL_PROBE_FEDERATION_PROTOCOL=saml for SAML. Complete strict SSO acceptance
remains pending.

## Recovery fault probe

[Recovery verification](recovery.md) describes the isolated local fault probe and
the demonstrated stop/drain/credential-retirement sequence. Run
`pnpm run probe:openfga-recovery` with a free loopback port 8080. Railway controls
and SDK recovery integration remain pending.
