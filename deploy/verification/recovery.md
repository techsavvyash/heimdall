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
