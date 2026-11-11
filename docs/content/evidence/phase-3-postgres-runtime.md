---
title: Phase 3 public PostgreSQL runtime storage
date: 2026-11-11
---

Both development SDKs now supply the durable storage bundle required by the
[[phase-3-public-runtime|public audited bearer runtime]]. Products supply their
existing PostgreSQL pool, fixed product/environment and explicit audit sink IDs.
They no longer implement readiness, identity gate or audit append callbacks for
this composition. This is Phase 3 progress, not first-release qualification.

```mermaid
sequenceDiagram
  participant Product
  participant SDK
  participant PG as Product PostgreSQL
  participant Services as ZITADEL and OpenFGA
  Product->>SDK: Construct storage with pool, scope and sinks
  SDK-->>Product: Scoped ports, no I/O or migration
  Product->>SDK: Construct runtime with config and credentials
  SDK->>PG: Read-only primary and schema readiness
  SDK->>Services: Discover issuer and verify pinned model
  Product->>SDK: Verify bearer token
  SDK->>PG: Read scoped identity denial
  SDK->>Services: Verify signature and current human status
  SDK->>PG: Commit authentication event and initial delivery rows
  PG-->>SDK: Committed receipt
  SDK-->>Product: Verified principal after release guards
```

## Developer wiring

```ts
import { createHeimdall } from "@heimdall/sdk";
import { createPostgresStorage } from "@heimdall/sdk/postgres";

const storage = createPostgresStorage(pool, {
  productId: "ledger", environment: "production", auditSinks: ["siem"],
});
const h = await createHeimdall(configJSON, manifestJSON, {
  policyVersion: "identity-v1",
  credentials: { clientSecret: loadClientSecret, openfgaKey: loadOpenFGAKey },
  storage,
}, { signal: startupSignal });
const actor = await h.tokens.verify(accessToken, { signal: request.signal });
```

```go
import (
    heimdall "github.com/techsavvyash/heimdall/sdk/go"
    "github.com/techsavvyash/heimdall/sdk/go/postgres"
)

storage, err := postgres.NewStorage(db, postgres.StorageOptions{
    ProductID: "ledger", Environment: "production", AuditSinks: []string{"siem"},
})
if err != nil { return err }
h, err := heimdall.New(ctx, configJSON, manifestJSON, heimdall.Options{
    PolicyVersion: "identity-v1",
    Credentials: heimdall.Credentials{ClientSecret: loadClientSecret, OpenFGAKey: loadOpenFGAKey},
    Storage: storage,
})
if err != nil { return err }
actor, err := h.Tokens().Verify(request.Context(), accessToken)
```

Pool construction, TLS/connection limits, explicit migration, credentials and pool
shutdown remain product responsibilities. The existing migration command installs
the immutable session-v1 schema before startup. No encryption key is needed for
bearer-only storage. Full sessions still require encryption and lifecycle wiring.

| Responsibility | Owner |
| --- | --- |
| Pool, migration and credential provisioning | Product deployment |
| Primary/schema/checksum readiness | SDK |
| Durable identity denial and exact-session invalidation | SDK |
| Event shape and SDK-event semantic validation | SDK |
| Atomic event/outbox append and committed receipt | SDK |
| Explicit initial sink identifiers | Product configuration |
| SIEM transport and delivery scheduling | Pending SDK delivery API plus product scheduler |
| Tenant business predicates and business transactions | Product |

The runtime role needs schema USAGE; SELECT on schema_metadata, migrations,
identity_denials, records, audit_events and audit_deliveries; INSERT/UPDATE on
identity_denials; DELETE on records; and INSERT on audit_events/audit_deliveries.
No schema-changing privileges or audit UPDATE/DELETE are needed. Readiness checks
schema/primary compatibility, not every privilege or future permission change.
Storage operations fail closed when required access fails. Scope is an application
boundary among trusted backends, not database isolation from a hostile process.

Sink lists are captured at construction. An empty list stores events with no
delivery rows. A sink name does not configure a transport or establish SIEM
compliance. Existing event routing is immutable. This factory validates mandatory
SDK events; custom event registration is still pending. Callbacks are trusted
backend ports, not public endpoints accepting user-provided actor assertions.

## Evidence and limits

Pinned PostgreSQL 17.11 run `heimdall-records-7ef1a6504e9b402e9cc2a5c687ec3929`
passed the complete storage/fault harness. New TS/Go tests use the exported runtime
and factory with a restricted role, actual PostgreSQL and fixture identity/model
services. Each language verifies active authentication, malformed-token refusal
and persistent denial of a warm verifier, yielding three committed events and
three initial SIEM rows. Mutating the original sink array does not reroute events.
Invalid audit shapes are rejected. Closing the caller-owned pool prevents runtime
startup. Factory construction opens no connection.

Shared `postgres-runtime.json` covers valid routing, invalid scope and duplicate,
empty or malformed sink IDs. Existing storage regression tests continue to cover
cross-language formats, transaction rollback, races and confirmed COMMIT-response
loss. Those fault tests exercise the reused storage implementations, not a new
full public-runtime fault matrix.

The packed TypeScript consumer gate now installs the tarball and its dependencies
in a fresh temporary workspace (preferring cached packages) with the 30-day release-age rule. It checks
core and PostgreSQL imports/declarations and rejects forged principal types.
The package remains private during development. This is not a final platform or
runtime support matrix.

The full workspace gate passed 1,258 TypeScript tests (27 optional live tests
skipped), Go race tests, formatting/lint/types/Knip and SDK builds. The updated
docs build validates 116 Mermaid diagrams across 80 pages.

Machine-readable storage evidence is committed at
`tests/integration/evidence/public-postgres-runtime-2026-11-11.json`. The earlier
Railway provider proof used recording storage; this checkpoint uses actual storage
with fixture services. A combined live-provider/durable-storage run remains open.
Sessions, public authorization, managed lifecycle, backpressure, custom audit and
SIEM delivery remain required release work. Phase 3 remains open.
