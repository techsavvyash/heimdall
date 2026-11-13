---
title: Phase 3 retained audit outbox admission
date: 2026-11-13
---

Both SDKs now expose the existing pending read, claim, defer, quarantine and
acknowledgement operations through the retained audit storage port. Pending
reads verify audit v4 or v5 compatibility in the same PostgreSQL snapshot
as the batch. Delivery mutations acquire the shared deployment lock and
verify readiness in the transaction before changing delivery rows.

```mermaid
flowchart LR
  Worker[Product delivery worker] --> Read[Read one compatible outbox snapshot]
  Read --> Claim[Claim with shared deployment lock and readiness check]
  Claim --> Send[Product sends the selected events]
  Send --> Update[Acknowledge or defer in a new gated transaction]
```

The worker supplies the sink, delivery timing and authenticated transport.
The existing attempt comparisons, partial acknowledgement and uncertain commit
semantics still apply. Schema admission applies even to an empty batch or a
transition that would affect zero rows. The port has no implicit scheduler.

Pinned PostgreSQL 17.11 run
`heimdall-capacity-backfill-70a47b2848e746dbaa85fe5b0de40a7b`
exercised both languages on v4 and v5. Each SDK read its pending event, claimed
one attempt, deferred it, acknowledged the receipt and observed an empty
pending batch. PostgreSQL confirmed the final attempt, retry and delivery
timestamps. Both SDKs refused all five calls during migration and through
read-only connections.
