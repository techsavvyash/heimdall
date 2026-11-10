# Enterprise identity-provider fixture

This service represents a customer's upstream SAML/OIDC IdP in live tests. It is not
part of the Heimdall runtime stack or a companion wrapper. The pinned upstream
Keycloak image is recorded in versions.json. It is a container fixture, not a pnpm
workspace dependency; no package release-age exception or dependency change is used.

Create a dedicated keycloak database owned by a non-superuser heimdall_keycloak role
on the private verification PostgreSQL service. Generate a separate random database
password and bootstrap administrator password. Store secrets outside the repository.
Create capability-enterprise-idp from the pinned image, publish port 8080 through
Railway HTTPS, and set:

```dotenv
PORT=8080
KC_HTTP_ENABLED=true
KC_HTTP_PORT=8080
KC_HOSTNAME=https://<fixture Railway hostname>
KC_PROXY_HEADERS=xforwarded
KC_DB=postgres
KC_DB_URL=jdbc:postgresql://capability-postgres.railway.internal:5432/keycloak
KC_DB_USERNAME=heimdall_keycloak
KC_DB_PASSWORD=<separate database password>
KC_BOOTSTRAP_ADMIN_USERNAME=heimdall-fixture-admin
KC_BOOTSTRAP_ADMIN_PASSWORD=<generated bootstrap password>
KC_DB_POOL_MAX_SIZE=10
KC_CACHE=local
JAVA_OPTS_KC_HEAP=-Xms64m -Xmx512m
```

Use start command `/opt/keycloak/bin/kc.sh start`, healthcheck `/realms/master` and
300-second startup allowance. The upstream image performs its configuration build
on startup; there is no custom image. The fixture uses a single instance and the
private database network without verified database TLS, matching the test-stack
boundary. Only Railway's HTTP proxy is exposed; no public direct database listener
is created. This is not a production Keycloak deployment recipe.

After successful initialization, remove both KC_BOOTSTRAP_ADMIN_* settings and
redeploy. The initialized test administrator remains available through its protected
credential for temporary realm setup; removing bootstrap env settings does not
revoke that account. Remove the fixture service/database and its privileged account
when the capability environment is retired.

## Reproduction

Set HEIMDALL_PROBE_IDP_URL to the fixture's exact HTTPS origin and load its protected
administrator password into HEIMDALL_PROBE_IDP_ADMIN_PASSWORD. Also supply the
verification ZITADEL issuer, bootstrap PAT and separate Login client PAT as described
in zitadel.md. Run `pnpm run probe:federation`.

The probe creates a uniquely named realm, confidential upstream OIDC client and
synthetic user. It creates a disposable ZITADEL organization, organization-scoped
OIDC provider and explicitly linked user. Auto-creation, auto-update and linking UI
are disabled. The test follows the pinned fixture's HTML login form with an isolated
in-memory cookie jar. This is protocol test automation, not a reusable browser or
an SDK HTML parser. It captures example.invalid callbacks without requesting them.

ZITADEL's intent retrieval verifies provider and subject; its session API checks the
intent and yields verified identity/organization factors. The probe then deletes only
its own realm, organization and sessions. For unknown creates or process termination,
inspect the run ID printed before writes before manually cleaning up. Passwords,
client secrets, upstream tokens and intent credentials are not logged or committed.

The current probe covers upstream OIDC intent evidence. SAML, downstream token binding,
strict tenant switching and production SDK integration remain separate acceptance work.

Sources: [Keycloak containers](https://www.keycloak.org/server/containers),
[reverse proxy](https://www.keycloak.org/server/reverseproxy),
[pinned release](https://github.com/keycloak/keycloak/releases/tag/26.8.2),
[ZITADEL intent API](https://github.com/zitadel/zitadel/blob/v4.19.4/proto/zitadel/user/v2/user_service.proto).
