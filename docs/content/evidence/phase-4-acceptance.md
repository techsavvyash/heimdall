---
title: Phase 4 browser sessions and product integration acceptance
date: 2026-11-13
---

Phase 4 covers #35–#37 and the initial reference integration for #41 and #42. Ledger and Support each embed the SDK in the product backend, use the shared ZITADEL and OpenFGA deployment with product-specific clients and stores, and scope business queries to the authorized workspace.

| Gate | Verified behavior | Evidence |
| --- | --- | --- |
| Browser sessions | Fetch and net/http adapters bind the browser to state, nonce and PKCE, claim the callback once, validate the returned identity, and issue an opaque cookie after audit and persistence. Concurrent refresh uses one owner, uncertain refresh requires sign-in, and logout prevents refresh from recreating a session. | [[phase-4-browser-login-http]], [[phase-4-session-refresh]], [[phase-4-custom-session-store]] |
| Product-owned persistence | Both SDKs encrypt and scope records, support key rotation, and conditionally invalidate the observed revision. The public conformance suites exercise storage predicates, expiry and revision non-reuse. Browser tests use a synchronized memory store and controlled OIDC provider, with a separate pinned PostgreSQL run. | [[phase-4-custom-session-store]] |
| Authorization | Public TypeScript and Go tests refuse fabricated principals, preserve the difference between a completed denial and an outage, and bind checks and effective roles to the pinned model. A stale owner tuple in Globex yields no effective roles and no business access. | [[phase-4-tenant-permission-checks]], [[phase-4-effective-roles]], [[phase-4-reference-backends]] |
| Workspace discovery | The core source scans candidates and the managed source checks active, unblocked workspace state. Both use current membership, audited candidate checks and signed scoped continuation. Tests cover sparse continuation, tampering and withholding results on dependency failure. | [[phase-4-workspace-discovery]], [[phase-4-managed-access]], [[phase-4-reference-backends]] |
| Railway product flow | Both reference products issued and retired browser sessions, exposed Acme alone in discovery, showed the owner role in Acme only, allowed an Acme business read, and denied Globex. Readback matched all durable decisions with their terminal outcomes. | [[phase-4-reference-backends]] |

The Railway integration uses the bundled PostgreSQL adapter and drives ZITADEL authentication through the Login API. The controlled-provider tests cover the full browser handler lifecycle including fault paths. Tenant-specific policy resolution, strict federation and account switching are part of [[design/enterprise-contract|Phase 6]]. Complete product onboarding flows and release qualification remain in the subsequent phases.

## Verification record

- `pnpm run check` passed on `c5c6512`. This includes TypeScript and Go tests, clean package import verification, and 224 Mermaid diagrams parsed from generated Quartz HTML.
- `python3 tests/integration/postgres-records.py` passed on `6e6b7b2` with pinned PostgreSQL 17.11. The run ID was `heimdall-records-1862e7d297804e4fa186ec8bb29688e5`.
- Railway verification used `6e6b7b2` SDK runtime sources. The later `c5c6512` commit adds browser rejection assertions and documentation.
- Railway run `35339739-e50c-44e8-8115-1bcb939b03da` verified Ledger deployment `f4507e3f-5450-4011-8477-723344a6800a` and Support deployment `8ec0d257-899c-4901-be8e-e4416c904a9b`. Readback found 36 Ledger events with 15 matching authorization requests and outcomes, and 32 Support events with 13 matching authorization requests and outcomes. Linked child decisions matched their role or discovery parent.
