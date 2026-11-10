---
title: Phase 1 evidence — identity encoding checkpoint
date: 2026-11-11
---

**Partial progress only. Phase 1 remains open.** This checkpoint establishes the shared identity encoding and real test runners. It does not validate authentication, sessions or deployed auth services.

## Implemented

- Canonical version-1 identity encoding in internal TypeScript and Go modules.
- Eighteen shared vectors covering Unicode, issuer distinctions, JSON escaping, exact byte limits and invalid input.
- Native malformed-Unicode rejection in both languages.
- Vitest empty-suite allowance removed.
- Seven sequential phase gates recorded in [[implementation-phases]].

## Local proof

Node 24.13.1, pnpm 12.10.1, frozen lockfile installation succeeded. Full workspace check passed: formatting, lint, types, Knip, Go formatting/vet, 22 Vitest tests, Go race tests, SDK build/declarations and Quartz build.

A temporary mutation replaced the first identity vector's expected ID with an incorrect value. Both test runners failed on that vector. The original fixture was restored. This proves the runners compare the shared golden result rather than merely exercising their own implementation.

## Remaining phase scope

Configuration/manifest schemas and semantic validation, stable typed errors/cancellation distinctions, tenant ID validation, session/cursor/operation/audit formats, persistence compatibility and further conformance fixtures remain. There is no verified-principal public API and no runtime service deployment in this checkpoint.

## Committed and deployed proof

Source commit: 41c578a870a08d879e9fa2ead075586768f24994. GitHub Actions completed successfully for that source commit.

Railway docs deployment: 0c06e53b-5f6d-46fe-bf5a-871fc31095bc, status SUCCESS. Verified HTTP 200 and expected content on both the [phase plan](https://docs-production-7570859e.up.railway.app/implementation-phases) and [this evidence page](https://docs-production-7570859e.up.railway.app/evidence/phase-1-progress).

This evidence update follows verification of that source deployment. Documentation deployment does not satisfy later runtime deployment gates.
