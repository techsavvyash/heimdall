---
title: Phase 1 evidence — authenticated record and cursor envelopes
date: 2026-11-11
---

**Partial progress; Phase 1 remains open.** Internal record encryption and cursor signing now share a concrete wire contract. This does not implement browser sessions, OIDC validation or workspace discovery.

## Implementation and proof

RecordCipher uses standard-library AES-256-GCM with random 96-bit nonces and 128-bit tags. Associated data binds product/environment, record kind, storage key and key ID. CursorSigner uses HMAC-SHA256 with a separate context. Both enforce canonical unpadded base64url and bounded input lengths. Exact formats are in contracts/README.md.

The corpus includes public synthetic keys, a reference vector and captured ciphertexts from the TS and Go SDK writers. Both language tests read both outputs. The HMAC writer outputs match byte for byte.

Tests reject altered nonce/ciphertext/tag, wrong context, changed key IDs even when key bytes are equal, unknown/retired keys, malformed/oversized envelopes, padding, whitespace and nonzero base64 padding bits. Rotation tests verify old-key reads and active-key writes. Key input buffers are copied.

The full workspace gate passed: 226 Vitest tests, Go race tests, lint/types/Knip/vet, SDK bundle/declarations and Quartz build.

## Operational limits and remaining work

The primitives do not constitute verified session authentication. Payload schema/scope/expiry checks follow successful cryptographic verification. Runtime composition must supply distinct stable encryption/signing keys and enforce rotation/usage policy across replicas. Go's random-nonce GCM API sets a per-key encryption limit; production key-usage enforcement is still an adapter release requirement. [Go GCM documentation](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce)

Phase 1 still needs operation/audit formats, persistence compatibility and complete configuration validation. Callback atomicity, refresh/logout races and live services remain later gates.

## Deployment proof

Source commit db8a88fe481e6b9ad33aee4af281fdc7440d6d04 passed GitHub Actions. Railway docs deployment 32610e33-ddc3-4586-950f-9dc988441c92 reached SUCCESS. The public crypto evidence page returned HTTP 200 with the expected 226-test result. This records a documentation deployment; runtime session/authentication integration remains unimplemented.
