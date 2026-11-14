---
title: Phase 1 evidence — session and cursor payloads
date: 2026-11-11
---

**Partial progress; Phase 1 remains open.** This checkpoint defines payload shapes and internal time/scope semantics. It does not supply authentication routes, encrypted storage or signed cursors.

## Verified behavior

Both languages consume identical schemas and fixtures. The tests exercise exact idle/absolute/token expiry, refresh-in-progress and expired-attempt behavior, missing refresh tokens, identity freshness including zero freshness, scope/policy mismatches and inconsistent timestamps.

Cursor fixtures cover product, environment, principal and model mismatch, future issue time, exact expiry and empty lifetime. Structural tests reject fractional/unsafe timestamps, unknown versions and malformed identifiers. Semantic tests first verify structural validity.

The focused tests passed: 189 Vitest tests and Go race tests. The full workspace gate runs before commit/push. Later cryptographic fixtures must prove envelope authenticity and interoperability; the current internal helpers require verified payloads and cannot be used as authentication by themselves.

## Remaining work

Authenticated encryption/signatures, operation/audit formats, persistence compatibility and complete endpoint/configuration validation still block Phase 1 completion. SessionStore atomicity, refresh coordination, login callbacks and live-service evidence are subsequent runtime gates.

## Deployment proof

Source commit d854fa97e816003dc86b4b45cceddfbd05c2f352 passed the full workspace gate and GitHub Actions. Railway docs deployment ec74960e-717b-4c69-b336-7a7da3543925 reached SUCCESS. The evidence page returned HTTP 200 with the expected 189-test result. This verifies the documentation checkpoint, not runtime session authentication.
