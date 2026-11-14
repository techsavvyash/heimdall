---
title: Phase 1 evidence — errors and schema conformance
date: 2026-11-11
---

**Partial progress; Phase 1 remains open.** This checkpoint adds internal error semantics and executable structural schemas. It does not implement network requests or authorization.

## Scope

- Ten stable error codes, sanitized JSON envelopes and opaque recovery operation IDs.
- Go typed errors support errors.Is through wrapping; TypeScript errors expose stable codes.
- Cancellation and deadline remain distinct before dispatch. A transport failure after a mutation may execute becomes outcome_unknown.
- Arbitrary cancellation causes are discarded rather than copied into error messages.
- Shared config/error schemas and fixture runners in both languages.
- Config rejects missing audience, unpinned model, unsupported schema version, insecure issuer and inline secrets.
- Trailing newline/carriage return/Unicode separator cases prevent TS/Go identifier-validation disagreement.

Schema checks are structural. Runtime issuer trust, endpoint parsing, service bindings, manifest compatibility, audit persistence and journal-driven recovery remain implementation work.

## Validation

The focused gate passed with 74 Vitest tests and Go race tests, plus formatting, lint, types, Knip and Go vet. Both runners compile the same JSON Schema 2020-12 files and run the same positive/negative corpora. Dependency versions are older than the 30-day minimum.

The full workspace check also passed, including SDK declarations and Quartz. A temporary mutation changed the valid production-config fixture to expect rejection; both runners failed on that named case, then the original fixture was restored. Railway verification follows the committed source deployment. Phase 1 still requires the remaining manifest, session, cursor, operation, audit and persistence contracts; this page does not close that gate.

## Deployment proof

Source commit 04a4f3d841874797a17095a9e7ae999c1b3a8788 passed GitHub Actions. Railway docs deployment ac5563d1-e1c5-4053-9a01-ac1b4a682ba0 reached SUCCESS. The public evidence URL returned HTTP 200 with the expected 74-test result. This records a documentation deployment, not an authentication-service deployment.
