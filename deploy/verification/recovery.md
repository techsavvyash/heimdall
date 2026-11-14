# OpenFGA recovery verification

Run `pnpm run probe:openfga-recovery` from the repository root with Node 24,
Docker and Compose v2, and a free loopback port 8080. It uses the pinned Compose
images, generates credentials in memory, creates a unique project, and removes
only that project's containers and volume. No existing Railway configuration is
changed. It is intentionally excluded from ordinary unit tests.

The probe covers two otherwise uncertain outcomes:

1. A loopback fault proxy forwards a write and observes upstream success, then
   destroys its client socket. The client sees a transport failure while a scoped
   read confirms the tuple exists. The proxy is a test fixture, not a runtime service.
2. A PostgreSQL transaction locks the tuple table. The probe dispatches another
   write and waits until pg_stat_activity confirms a database lock wait. It then
   kills its sole OpenFGA container and verifies that container is stopped.

With the old server stopped, the probe terminates its remaining database backends
and verifies zero remain (excluding the control connection and test lock holder).
Only then does it release the test lock. The deployment restarts as a new container
with a newly generated API key; the old key is absent from the new configuration.
The killed write remains absent. Delayed old-key requests receive 401. The earlier
committed tuple remains visible and is explicitly reconciled with the new key.
After confirmed deletion, a delayed old-key grant still cannot resurrect it.

## Scope of the demonstrated procedure

This is single-replica local deployment evidence, not an automatic SDK recovery
algorithm or a verified Railway operator runbook. Killing a worker or rotating a
key alone is not the demonstrated procedure. API requests already past authentication
must also lose their server execution and database transaction paths. All credential
paths must be inventoried and retired: this fixture uses only preshared authentication,
with no other service instances, direct database writers, queues or credential brokers.

A real deployment must keep affected workspace gates closed, stop all product
writers and prevent replacement writers, stop every old OpenFGA replica, establish
database transaction quiescence, retire every old API credential, then restart under
controlled recovery credentials. Delayed requests outside the old server must not
remain authorized. Auto-scaling/restarts, rolling overlap, proxy retries and independent
operators cannot be assumed absent. Recovery reconciles the recorded immutable
intent and postconditions, not whatever state happens to be convenient.

The SDK must later persist the authorized recovery action and evidence, recheck the
operation generation and current workspace gate, and atomically record the result
before unblocking. Failed or incomplete quiescence leaves it blocked. There is no
force-unblock flag. Because this deployment shares an API credential across stores,
the maintenance impact is deployment-wide; this is not per-workspace fencing.

Railway-specific replica/process/database controls, recovery authorization and
journal integration remain unverified. The managed-lifecycle release gate stays
open until those required deployment and SDK tests pass.

## Railway verification run

The dedicated single-replica Railway test passed on November 11, 2026; see
../../docs/content/evidence/phase-2-railway-recovery.md. The opt-in command is:

    python3 tests/integration/openfga-recovery-railway.py --execute-verification-recovery

This is a maintenance operation against exact Heimdall verification IDs embedded
in the probe. It is not a generic production command. It requires Python 3,
Railway CLI credentials and the registered SSH identity (override its path with
HEIMDALL_PROBE_SSH_KEY). Supply HEIMDALL_PROBE_OPENFGA_KEY and a distinct generated
HEIMDALL_PROBE_NEW_OPENFGA_KEY via a protected environment. Save the replacement
in operator secret storage before starting; after the variable change it remains
the active credential, including if a later assertion fails. Never restore the
retired key or roll back to an old deployment's variables. The script does not
print keys or edit the operator's secret files.

Preflight requires one replica and the exact sole current preshared key. The run
creates only a test store, but the table lock and maintenance affect the entire
verification OpenFGA service. It removes the old deployment, waits for REMOVED,
checks that no listed deployment is active/building, drains database backends,
retires the old key with skip-deploys, then starts a replacement through
serviceInstanceDeployV2 using current configuration. It verifies health before
reconciliation and removes its owned store. ZITADEL and docs are not targeted.

A bounded observation deadline is not authorization to launch another recovery
or replacement. Inspect the known deployment/request state first. If an operator
interrupts the probe, inspect the test lock, service/deployment state, current
credential and owned store before resuming. Keep affected product gates closed.
The script's success/failure is not a signed recovery receipt and cannot itself
clear the future SDK's journal. The topology assumptions and later SDK gates in
the evidence page remain mandatory.

## Combined SDK journal drill

Run from the repository root with Node, pnpm, Go, Docker Compose, and a free
loopback port 8080.

    HEIMDALL_PROBE_SDK_RECOVERY=1 python3 tests/integration/postgres-records.py

This runs the same isolated service controls and recovers two durable workspace
operations using the TypeScript and Go provisioning APIs. Both PostgreSQL
instances are disposable. See the workspace recovery evidence in Quartz for
the verified scope and outcomes.
