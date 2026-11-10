# Pinned ZITADEL capability environment

This is a test deployment, not the production recipe. Use the API and Login UI image
identities in versions.json. The existing Compose file covers only OpenFGA/PostgreSQL.
These instructions describe the live Railway setup; full local reproduction is pending.

## Bootstrap API

Create capability-zitadel in the verification environment. Publish its port 8080 via
Railway HTTPS, attach a volume at /zitadel/bootstrap, and use these configuration keys.
Values in angle brackets must be replaced; never commit generated credentials.

```dotenv
PORT=8080
ZITADEL_PORT=8080
RAILWAY_RUN_UID=0
ZITADEL_EXTERNALDOMAIN=<API Railway domain without scheme>
ZITADEL_EXTERNALPORT=443
ZITADEL_EXTERNALSECURE=true
ZITADEL_TLS_ENABLED=false
ZITADEL_MASTERKEY=<32 random ASCII characters>
ZITADEL_DATABASE_POSTGRES_HOST=capability-postgres.railway.internal
ZITADEL_DATABASE_POSTGRES_PORT=5432
ZITADEL_DATABASE_POSTGRES_DATABASE=zitadel
ZITADEL_DATABASE_POSTGRES_USER_USERNAME=heimdall_zitadel
ZITADEL_DATABASE_POSTGRES_USER_PASSWORD=<separate generated password>
ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE=disable
ZITADEL_DATABASE_POSTGRES_ADMIN_USERNAME=heimdall_probe
ZITADEL_DATABASE_POSTGRES_ADMIN_PASSWORD=<existing bootstrap database password>
ZITADEL_DATABASE_POSTGRES_ADMIN_SSL_MODE=disable
ZITADEL_DATABASE_POSTGRES_ADMIN_EXISTINGDATABASE=openfga
ZITADEL_FIRSTINSTANCE_INSTANCENAME=Heimdall Verification
ZITADEL_FIRSTINSTANCE_ORG_NAME=Heimdall Verification
ZITADEL_FIRSTINSTANCE_ORG_HUMAN_PASSWORD=<random password satisfying default complexity>
ZITADEL_FIRSTINSTANCE_ORG_MACHINE_MACHINE_USERNAME=heimdall-bootstrap
ZITADEL_FIRSTINSTANCE_ORG_MACHINE_MACHINE_NAME=Heimdall verification bootstrap
ZITADEL_FIRSTINSTANCE_ORG_MACHINE_PAT_EXPIRATIONDATE=<future RFC3339 date>
ZITADEL_FIRSTINSTANCE_PATPATH=/zitadel/bootstrap/admin.pat
ZITADEL_FIRSTINSTANCE_ORG_LOGINCLIENT_MACHINE_USERNAME=heimdall-login
ZITADEL_FIRSTINSTANCE_ORG_LOGINCLIENT_MACHINE_NAME=Heimdall verification login client
ZITADEL_FIRSTINSTANCE_ORG_LOGINCLIENT_PAT_EXPIRATIONDATE=<future RFC3339 date>
ZITADEL_FIRSTINSTANCE_LOGINCLIENTPATPATH=/zitadel/bootstrap/login-client.pat
ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_REQUIRED=true
ZITADEL_DEFAULTINSTANCE_FEATURES_LOGINV2_BASEURI=https://<Login UI domain>/ui/v2/login/
ZITADEL_OIDC_DEFAULTLOGINURLV2=https://<Login UI domain>/ui/v2/login/login?authRequest=
ZITADEL_SAML_DEFAULTLOGINURLV2=https://<Login UI domain>/ui/v2/login/login?samlRequest=
ZITADEL_OIDC_DEFAULTLOGOUTURLV2=https://<Login UI domain>/ui/v2/login/logout?post_logout_redirect=
ZITADEL_LOG_LEVEL=warn
ZITADEL_TELEMETRY_ENABLED=false
```

Set start command `/app/zitadel start-from-init --masterkeyFromEnv` and healthcheck
`/debug/ready` (300-second startup allowance). Use individual database fields: the
bootstrap creates its dedicated database/user. A DSN-only configuration does not
perform that bootstrap. The existing admin role is a test superuser; it is not the
runtime role. Private-network database traffic is unencrypted in this test environment.

A hex-only initial human password failed the default uppercase requirement. Keep the
policy intact and supply a generated password including uppercase, lowercase, digits
and symbols. Do not erase the database to retry a password-policy error.

Download the two PAT files through authenticated Railway file access to protected
files outside the repository (directory mode 700, files mode 600). The bootstrap
machine has IAM_OWNER; the separate Login client has IAM_LOGIN_CLIENT. Never give the
bootstrap PAT to a product SDK or the Login UI. The observed test PATs expire on
2026-12-11; rotate/revoke through ZITADEL before expiry. Editing first-instance env
settings after setup does not rotate existing credentials.

After successful initialization, change the API start command to
`/app/zitadel start --masterkeyFromEnv`. Remove ADMIN_PASSWORD, ADMIN_USERNAME,
ADMIN_EXISTINGDATABASE under ZITADEL_DATABASE_POSTGRES and the first human password
from the service environment. Deploy current settings and recheck readiness and
provisioning. Preserve the master key and application database password securely.
Bootstrap PAT files remain privileged recovery material on the restricted volume;
remove/revoke them when verification no longer needs them.

## Upstream Login UI

Create capability-zitadel-login using the pinned upstream Login image. Use its default
entrypoint and port 3000. Set:

```dotenv
PORT=3000
HOSTNAME=0.0.0.0
NEXT_PUBLIC_BASE_PATH=/ui/v2/login
ZITADEL_API_URL=https://<API Railway domain>
ZITADEL_SERVICE_USER_TOKEN=<dedicated Login client PAT>
ZITADEL_SESSION_COOKIE_SECRET=<separate random 64-character hex value>
CUSTOM_REQUEST_HEADERS=x-zitadel-instance-host:<API Railway domain>,x-zitadel-public-host:<Login UI domain>
```

The API URL is public HTTPS; the upstream UI's Connect client uses HTTP/1.1. The UI
is an upstream ZITADEL component, not a Heimdall wrapper service. A request to
`/ui/v2/login/login` without an authentication request returns 400. This is only
missing-request rejection evidence, not a completed human login.

For this separate-domain, single-instance topology, add the exact Login hostname
with InstanceService/AddTrustedDomain before browser testing. The fixed custom
headers override browser-supplied instance/public hosts on outbound API calls.
The pinned Login transport applies them after its inferred host headers. Without
this configuration, a valid browser auth request returned an instance-not-found
error even though the Login startup credential check passed. Do not reuse these
fixed headers for a Login UI serving multiple ZITADEL instances. See the browser
evidence for the deployed result.

## Capability probe

Load the protected bootstrap PAT into HEIMDALL_PROBE_ZITADEL_PAT and set
HEIMDALL_PROBE_ZITADEL_ISSUER to the exact API HTTPS issuer. Run `pnpm run probe:zitadel`.
Do not put secrets in shell history. Each request has a 15-second deadline and no
mutation retries. The run ID is printed before writes; two uniquely named projects
are created, read and deleted. If a create response is lost, inspect projects with
that run ID before manual cleanup. Cleanup only this probe's resources. Output is
sanitized and explicitly lists unverified release requirements.

Sources: [tagged upstream Compose](https://github.com/zitadel/zitadel/blob/v4.19.4/deploy/compose/docker-compose.yml),
[tagged configuration](https://github.com/zitadel/zitadel/blob/v4.19.4/cmd/defaults.yaml),
[Project v2 API](https://github.com/zitadel/zitadel/blob/v4.19.4/proto/zitadel/project/v2/project_service.proto),
[Railway volume permissions](https://docs.railway.com/volumes).

## SCIM Users lifecycle probe

With the same protected issuer and bootstrap PAT environment, run `pnpm run probe:scim`. This creates two disposable organizations and synthetic identities, tests tenant resource paths, external-ID lookup, default username uniqueness, repeated disable/reactivation/deletion and authoritative user state, then deletes its owned organizations. The initial checkpoint used a bootstrap credential; the current probe uses the dedicated role described below for SCIM operations. Run only in the verification environment. Use the printed run ID to inspect uncertain creates before manual cleanup. The committed sanitized evidence distinguishes upstream state visibility from later SDK worker/session guarantees.

## Narrow SCIM credential role

The current probe creates a machine user in each disposable organization, assigns
ORG_HEIMDALL_SCIM through the v2 InternalPermissionService/CreateAdministrator API,
and issues a one-hour PAT. Provisioning-domain metadata is set on each machine
before provisioning; SCIM user operations use that organization's token. Bootstrap
credentials are retained only for creating/deleting organizations and assigning the
initial role. Deleting the disposable organizations removes their machines too.
No temporary directory PAT is printed or saved.

The custom role has user.read, user.write and user.delete. These permissions apply
to user-management APIs, not exclusively to SCIM URLs. Treat this as a powerful
organization lifecycle credential, never a product session credential. The probe
checks own-organization lifecycle, denied cross-organization read/update/delete,
denied administrator grants, and provisioning-domain lookup isolation. It is not
an exhaustive permission/security audit.

The deploy/verification/zitadel-role-permissions.json file preserves all 27 default
role mappings from the pinned v4.19.4 cmd/defaults.yaml and appends the custom role.
The source YAML SHA-256 is
07dbde584dadeb65d78d1860a09e6237746acdd22e42413513484b4b44b0ae95.
Do not replace the map with only the new role: ZITADEL's environment setting replaces
the complete map, including operator and Login UI permissions.

Set ZITADEL_INTERNALAUTHZ_ROLEPERMISSIONMAPPINGS to the JSON file content. Run the
pinned API once with `/app/zitadel start-from-setup --masterkeyFromEnv` to synchronize
roles to the database, verify readiness, then return to
`/app/zitadel start --masterkeyFromEnv` and redeploy. This uses the existing application
database credential; bootstrap database superuser credentials are not restored.
Keep the complete role configuration in deployment configuration. On a service
upgrade, compare the new upstream defaults and preserve their required changes;
do not blindly reuse an old default-role snapshot.

Source: [pinned internal authorization mappings](https://github.com/zitadel/zitadel/blob/v4.19.4/cmd/defaults.yaml),
[SCIM permission mapping](https://github.com/zitadel/zitadel/blob/v4.19.4/internal/api/scim/authz.go),
[administrator API](https://github.com/zitadel/zitadel/blob/v4.19.4/proto/zitadel/internal_permission/v2/internal_permission_service.proto).

## Human OIDC capability probe

Run `pnpm run probe:oidc` with HEIMDALL_PROBE_ZITADEL_ISSUER, the protected bootstrap
HEIMDALL_PROBE_ZITADEL_PAT and the separate HEIMDALL_PROBE_ZITADEL_LOGIN_PAT from the
Login client file. The Login client credential verifies a generated synthetic human
password through the upstream session API and completes the OIDC request; it is not
a product runtime credential. Never expose it in product configuration.

The probe creates a disposable organization, human, two projects/clients and provider
sessions. It tests code/PKCE, signatures/audiences, refresh, account disable/delete,
provider-session deletion and explicit token revocation. It captures HTTPS callback
redirects to example.invalid without making requests to those domains. No browser
Login UI or external enterprise IdP is exercised. Temporary secrets stay in memory.
Owned sessions and organization are deleted on exit; inspect the printed run ID
before cleanup after uncertain creation or process termination. Provider-session
deletion was observed to allow refresh into a new active access token; explicit
refresh-token revocation prevented that. See the published Phase 2 OIDC evidence.

## Browser OIDC probe

Run `pnpm run probe:browser-oidc setup` with the same issuer and two protected PATs
as the OIDC probe, plus HEIMDALL_PROBE_BROWSER_STATE set to an absolute path in a
protected directory outside the repository. Setup creates a disposable organization,
user and confidential client, saving credentials/PKCE state with mode 600. It prints
only the synthetic interactive username/password and authorization URL. Open that URL
in a browser, sign in, and capture the callback URL. The example.invalid callback is
intentionally unreachable; its DNS error is not an authentication failure.

Pass that exact callback as HEIMDALL_PROBE_BROWSER_CALLBACK to
`pnpm run probe:browser-oidc verify`. Keep it out of logs and source files. This
standalone verifier checks state, code/PKCE exchange, signed tokens and provider
session binding; it does not implement the SDK callback. Finally run
`pnpm run probe:browser-oidc cleanup` to remove owned sessions/organization and the
protected file. Setup will not overwrite an existing fixture file. If interrupted
before a create response is saved, inspect resources with the run ID before cleanup;
there are no mutation retries or uncertain-write guarantees in this test helper.

Sources: [Login domain setup](https://zitadel.com/docs/guides/integrate/login-ui/login-app),
[pinned outbound header ordering](https://github.com/zitadel/zitadel/blob/v4.19.4/apps/login/src/lib/zitadel.ts),
[pinned trusted-domain API](https://github.com/zitadel/zitadel/blob/v4.19.4/proto/zitadel/instance/v2/instance_service.proto).

The OIDC probe also tests self-service session/identity reads with a second code flow
requesting the ZITADEL API audience in addition to the product audience. It checks
product-only rejection, another user's session denial and other-product introspection
denial. The federation probe checks identical session factors with the user token.
These reads do not expose provider ID/upstream subject: full strict-SSO provenance
still requires the separate verified-intent flow.

## Narrow federation login role

The complete role map now also includes IAM_HEIMDALL_FEDERATION with session.read,
session.write and session.link. Synchronize role updates with the same one-time
start-from-setup procedure above, then restore the ordinary start command. The role
is instance-scoped because session creation checks permission before a user is set.
It is for SDK-controlled strict federation flows, not a tested replacement credential
for the entire upstream Login UI and not a tenant-isolation boundary.

Set HEIMDALL_PROBE_NARROW_LOGIN=true for the federation probe. It creates a disposable
machine/PAT, proves roleless session creation fails, grants the narrow role using
bootstrap authority, and runs the protocol chain with the new credential. User
reads and self-escalation must fail. Cleanup revokes the instance grant before
removing the organization. The ordinary Login PAT remains available for cleanup.
