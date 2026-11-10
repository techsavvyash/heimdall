---
title: Phase 1 evidence — immutable operations and retry states
date: 2026-11-11
---

**Partial progress; Phase 1 remains open.** This checkpoint implements internal intent digests and a conservative transition contract, plus intent/journal schemas. It is not a durable worker or recovery implementation.

## Verified behavior

Both SDKs consume the same golden SHA-256 vectors and state matrix. Intent fingerprints include product/environment, kind, actor, target, request workspace, name, sorted roles and manifest version. Separate idempotency digests bind the caller's key to product/environment/kind/actor. Creation excludes the later allocated workspace ID, keeping request retries stable.

Fixtures cover changed scope/actor/version, role-order equivalence, Unicode escaping, invalid owner bootstrap, empty keys, blank Unicode names and controls. The transition matrix rejects illegal transitions, stale workers, cancellation after dispatch and retry of uncertain writes. Uncertain operations have no automatic transition out.

The shared journal schema requires intent/digests, allocated workspace ID, generation and dispatch count. Dispatch/confirmation/completion states require a dispatch history; cancelled records prohibit one. Schema references resolve from checked-in resources in both test runners.

The full workspace gate passed with 341 Vitest tests, Go race tests, lint/types/static checks, SDK build/declarations and Quartz.

## Remaining evidence

Database uniqueness, atomic generation CAS, durable dispatch markers, immutable records and timestamp/fingerprint consistency remain persistence gates. This function cannot fence an external write. Definitive rejection needs service-specific proof; lost responses stay uncertain. Operator recovery requires live quiescence evidence and remains a release blocker until demonstrated.

## Deployment proof

Source commit ef23516d6b6d571123469fdc28b121f97083d9fa passed GitHub Actions. Railway docs deployment 17a2898a-52da-4ca1-9b5b-b8e33c5b1db7 reached SUCCESS. The public operation evidence page returned HTTP 200 with the expected 341-test result. This verifies the documentation checkpoint; durable workers and live recovery remain unimplemented.
