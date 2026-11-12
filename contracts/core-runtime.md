# Development core runtime API

This implemented development entry point composes the Phase 3 foundations. It is
not the first release: browser sessions, tenant permission/role/discovery APIs,
managed administration and enterprise workers are not exposed yet. No stub public
methods advertise those capabilities.

TypeScript exports createHeimdall(configJSON, manifestJSON, options, {signal}).
Go exports heimdall.New(ctx, configJSON, manifestJSON, options). Both accept bounded
configuration/manifest JSON using the existing contract. Options contain a fixed
identity policy version, optional identity freshness (0–60000 ms, default 60000),
explicit client-secret/OpenFGA-key loaders and one scoped RuntimeStorage bundle.

RuntimeStorage declares product/environment, a read-only readiness callback, a
local identity-deny/invalidation gate and appendAudit. Scope must equal the loaded
configuration. The product's core-mode ports are trusted implementations of the
durable contracts: readiness must validate storage availability/compatibility and
scope; append must return only after commit; invalidation must persist its denial
or exact-session mutation. An arbitrary callback returning a receipt cannot prove
its own durability. The PostgreSQL storage composition below supplies these ports. Managed composition remains pending.
No startup migration, background worker, database pool ownership or shutdown of
caller-owned resources is implied by construction.

Startup validates all local inputs and captures functions/configuration before
its first await. It then checks storage readiness, loads issuer-pinned OIDC
metadata and verifies the exact configured OpenFGA model against the compiled
manifest. The entire construction has a five-second deadline or earlier caller
cancellation. Noncooperative readiness callbacks cannot delay the return beyond
the deadline, but the product must still honor cancellation to avoid leaking its
own work. Unknown callback errors become redacted unavailable. No runtime is
returned on a failed prerequisite.

The selected ZITADEL profile currently requires discovered introspection at the
issuer's /oauth/v2/introspect endpoint. An alternative same-origin route is refused,
not ignored. JWKS is taken from validated discovery. Client-secret validity and
client registration cannot be proved without an authentication request; those
fail closed during verification. Matching an OpenFGA model does not prove the
operator chose the intended external project/store when two policies coincide.

The public authentication operation is h.tokens.verify(rawToken, {signal}) in TS
and h.Tokens().Verify(ctx, rawToken) in Go. It fixes context to bearer and offers no
per-request issuer/audience/project/store/model override. It invokes the existing
audited authentication composition, including signature verification, human state,
local denial, bounded freshness, durable event receipts and post-commit release
guards. Malformed tokens are audited; invalid SDK call options (missing/invalid
signal or nil context) are rejected as programmer arguments before authentication.

Results contain verified identity and observation timestamps. TS returns a frozen
branded Principal and frozen facade; Go returns a Principal with private identity
and provenance fields and accessors. Caller-created objects/zero values do not
establish provenance. This does not grant workspace membership or permission, and
provenance alone does not imply timeless freshness. Public authorization boundary
checks will be tested when those APIs are implemented in Phase 4.

Core storage implementers can use auditEventDigest / heimdall.AuditEventDigest to
compute the shared receipt digest. This helper does not validate event semantics
or prove persistence. TS exports HeimdallError; Go exports Error and sentinel
errors for errors.Is. Credential material and upstream error messages are redacted.

```ts
import { createHeimdall } from "@heimdall/sdk";

const h = await createHeimdall(
  configJSON,
  manifestJSON,
  {
    policyVersion: "identity-v1",
    credentials: { clientSecret: loadClientSecret, openfgaKey: loadOpenFGAKey },
    storage: durableProductStorage,
  },
  { signal: startupSignal },
);

const actor = await h.tokens.verify(accessToken, { signal: request.signal });
// actor identifies a human; no tenant permission has been granted by this call.
```

```go
h, err := heimdall.New(ctx, configJSON, manifestJSON, heimdall.Options{
    PolicyVersion: "identity-v1",
    Credentials: heimdall.Credentials{
        ClientSecret: loadClientSecret,
        OpenFGAKey: loadOpenFGAKey,
    },
    Storage: durableProductStorage,
})
if err != nil { return err }
actor, err := h.Tokens().Verify(request.Context(), accessToken)
```

The examples name product-owned durable ports, not in-memory production defaults.
There is no extra Heimdall service. The TypeScript package remains private and
ESM-only during development. Shared runtime-startup.json covers startup failures;
public package tests and live-service probes exercise the actual exported APIs.

## PostgreSQL storage composition

TS exports `createPostgresStorage(pool, {productId, environment, auditSinks})` from
`@heimdall/sdk/postgres`. Go exports `postgres.NewStorage(db, StorageOptions)` from
`github.com/techsavvyash/heimdall/sdk/go/postgres`. Both return RuntimeStorage for the
existing runtime constructor. No encryption key or session payload validator is
needed for bearer authentication. Neither exposes an unfinished session API.

Construction validates scope and sink routing, captures the values, and performs
no I/O. Runtime startup invokes the existing read-only primary/schema/checksum
readiness check. Readiness is not an exhaustive privilege audit and does not fence
later schema or permission changes. Storage operations fail closed if privileges
or availability subsequently fail. The caller supplies one existing Pool/*sql.DB,
initializes the immutable session-v1 schema and explicitly prepares/backfills/
activates audit v2, prepares/accounts/activates capacity v3, and prepares/backfills/activates retained audit v4 with deployment commands beforehand. It retains ownership of
connection limits, TLS configuration and pool shutdown. Unactivated v1, v2, v3 or migrating
storage is refused by the public factory's readiness, audit append and identity storage ports.
No DDL or background delivery runs on construction or verification.

Go `postgres.NewConnector(dsn)` is the recommended connection entry point. It
snapshots lib/pq configuration without opening a database connection; `sql.OpenDB(connector)` gives the
application its ordinary pool. Every new connection is bounded by five seconds
or earlier caller cancellation, including TLS and PostgreSQL startup. A shorter
DSN `connect_timeout` remains shorter. Cancellation closes the startup socket;
watchers are stopped and joined before handing a healthy connection to the pool.
DSN credentials and TLS settings are preserved, and driver errors are sanitized.
Both deployment CLIs use this connector. NewStorage still accepts caller-owned
pools but cannot retrofit their driver: custom pools must independently provide
bounded startup and cancellation. Bare lib/pq sql.Open without connect_timeout
fails that requirement; even a static timeout alone does not prove prompt caller
cancellation during startup. This is a Go driver correction, not a new service or
change to TypeScript storage semantics.

The pool role needs schema USAGE; SELECT on schema_metadata, migrations,
identity_denials, records, audit_events and audit_deliveries; INSERT and UPDATE on
identity_denials; DELETE on records; INSERT on audit_events/audit_deliveries; and
SELECT, INSERT and UPDATE on audit_decisions. Capacity additionally requires USAGE
on heimdall_capacity; SELECT on policy, upgrade and backfill; SELECT/INSERT on
event_charges; SELECT/INSERT/UPDATE on reservations; and UPDATE only on the six
used/reserved counter columns of policy. Triggers derive counters; direct counter
changes are refused. Do not grant accounting DELETE, receipt writes or limit/state
updates to runtime. Retention requires USAGE on heimdall_retention; SELECT on
its capacity, upgrade, backfill, event_identities, decision_identities, event_holds
and retirements tables; INSERT/UPDATE on event_identities and decision_identities;
and UPDATE only on used_rows/reserved_rows of capacity. Do not grant retained
identity DELETE, hold/marker writes or metadata quota-limit updates.
It does not need decision DELETE,
metadata writes, schema-changing privileges, audit UPDATE/DELETE or session
INSERT/UPDATE for this bearer-only composition. This is a shared trusted backend
scope, not database row-level isolation against a hostile product process.

Audit sinks are an explicit list (Go nil is equivalent to empty), at most 32 unique
ASCII identifiers. Empty routing still durably stores every event, without delivery
rows. Sink identifiers do not configure exporters or prove SIEM delivery. Initial
routing is copied and immutable for an existing event. Event validation includes
bundled shape and mandatory SDK-event semantics. Product custom event registration
is not exposed here. Storage callbacks remain trusted backend ports; they do not
turn caller-provided actor data into verified identity.

```ts
import { createHeimdall } from "@heimdall/sdk";
import { createPostgresStorage } from "@heimdall/sdk/postgres";

const storage = createPostgresStorage(pool, {
  productId: "ledger",
  environment: "production",
  auditSinks: ["siem"],
});
const h = await createHeimdall(
  configJSON,
  manifestJSON,
  {
    policyVersion: "identity-v1",
    credentials: { clientSecret: loadClientSecret, openfgaKey: loadOpenFGAKey },
    storage,
  },
  { signal: startupSignal },
);
```

```go
connector, err := postgres.NewConnector(databaseDSN)
if err != nil { return err }
db := sql.OpenDB(connector)
defer db.Close() // application lifetime, not each request
storage, err := postgres.NewStorage(db, postgres.StorageOptions{
    ProductID: "ledger", Environment: "production", AuditSinks: []string{"siem"},
})
if err != nil { return err }
h, err := heimdall.New(ctx, configJSON, manifestJSON, heimdall.Options{
    PolicyVersion: "identity-v1",
    Credentials: heimdall.Credentials{ClientSecret: loadClientSecret, OpenFGAKey: loadOpenFGAKey},
    Storage: storage,
})
```

The SDK handles readiness, identity denial and invalidation, event validation,
atomic event/outbox append and committed receipts. Products still configure their
pool, credentials and deployment, and enforce tenant business predicates. Public
SIEM delivery, backpressure policy, retention, session orchestration, authorization
and managed lifecycle composition remain release work, not implicit guarantees of
this development factory.

## Activated v4 audit and identity admission

Public audit append uses a shared deployment advisory transaction lock and checks
v4 metadata/ledger, retention preparation receipts and capacity policy inside the same transaction before writing. The coordinated
deployment commands take that lock exclusively. A factory that previously passed
startup does not cache audit-write admission: migrating or incompatible state
rejects even an exact receipt retry, with no new event/slot/outbox writes. Five-second
append cancellation and explicit ambiguous-COMMIT errors remain unchanged. This
coordinates public SDK audit and identity invalidation writes with deployment. Identity
reads include v4 readiness in the same statement snapshot. Internal session ports,
direct SQL, old processes and remote requests still require external drain.

Authorization requested/completed events use atomic decision-slot persistence.
Every new event gets one server-derived capacity charge. Requests also reserve
the full payload and retained-metadata recovery allowance; completion releases unused reservations atomically. Compact event and decision identities preserve exact retries after payload retirement. Ordinary
authentication and other supported SDK events also consume capacity. Exhaustion
returns audit_unavailable and rolls back event/slot/outbox/accounting together.
The public append port rejects authorization outcome_unknown, which requires the
separate recovery administration pair. Actor verification and event origin remain
trusted SDK/product-layer responsibilities; this is not a public permission API.

Managed installations can be initialized against recognized ready audit v1, v2, v3 or v4.
`--status managed-v4` (internal CheckManagedRetentionReadiness/checkManagedRetentionReadiness)
checks audit v4 and managed v1 metadata/ledgers in one primary SQL snapshot.
Legacy `--status session` and `--status managed` deliberately retain v1 semantics
for deployment compatibility; they are not the current public factory's readiness.
Use `--status audit-v4` for the public bearer storage composition. A read-only
connection is rejected before migration starts a read/write transaction.

Public factories use retained audit writes and strict identity admission. Identity
invalidation locks deployment admission before its identity lock; both identity-wide
denial and exact-session deletion refuse incompatible storage. Go verifies writable
primary connection state before BEGIN to prevent transaction options from bypassing
read-only configuration. No automatic migrations, recovery scheduler or retention
administration API is exposed. Managed audit composition and historical recovery
inspection remain separate internal integration work; v4 readiness does not certify
them. `tests/integration/public-runtime-v4.py` verifies the actual public factories
with restricted credentials, cross-language retries and confirmed COMMIT loss.
