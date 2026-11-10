---
title: Phase 1 evidence — audit fingerprints and receipt binding
date: 2026-11-11
---

**Partial progress; Phase 1 remains open.** Internal TS and Go helpers now compute identical audit fingerprints and bind local/export receipts to expected scope. They do not append or export events.

## Verified behavior

Independent reference vectors cover reordered keys, negative zero, fractional/tiny numbers, safe-integer boundaries, Unicode, nested attributes, array order and changed event/scope/timestamp. Both language implementations match the golden SHA-256 outputs. Invalid Unicode, excessive size/depth, unsupported values and invalid keys fail.

Receipt fixtures cover exact matches, wrong product/environment/event/timestamp/digest, full and partial export acknowledgements, reordering, duplicates and unsubmitted IDs. A malformed acknowledgement cannot advance any checkpoint; a valid partial acknowledgement identifies only its acknowledged subset.

The full workspace gate passes with 523 Vitest tests, Go race tests, formatting/lint/type/static checks, SDK bundles/declarations and Quartz. No dependency was added.

```mermaid
sequenceDiagram
  participant SDK as Product SDK
  participant Store as Durable store (later phase)
  participant Sink as SIEM receiver (later phase)
  SDK->>SDK: Freeze event and compute v1 fingerprint
  SDK->>Store: Append immutable event
  Store-->>SDK: Scoped receipt with same digest and timestamp
  SDK->>SDK: Verify receipt binding before releasing result
  Store->>Sink: Submit scoped batch E1 and E2
  Sink-->>Store: Acknowledge E2 only
  Store->>Store: Verify IDs belong to batch; keep E1 pending
```

## Remaining proof

The digest is a checksum, not a signature or commit receipt. Atomic append, conflict enforcement, raw-ingestion duplicate-key rejection, authenticated transport and durable partial checkpoints remain persistence/enterprise implementation work. Immutable event construction and raw payload limits must be wired into those paths. No live SIEM compatibility claim is made.

## Deployment proof

Pending verification for this checkpoint.
