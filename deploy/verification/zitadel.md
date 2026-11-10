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
```

The API URL is public HTTPS; the upstream UI's Connect client uses HTTP/1.1. The UI
is an upstream ZITADEL component, not a Heimdall wrapper service. A request to
`/ui/v2/login/login` without an authentication request returns 400. This is only
missing-request rejection evidence, not a completed human login.

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
