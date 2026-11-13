---
title: Phase 6 product audit delivery
date: 2026-11-14
---

The TypeScript PostgreSQL adapter exposes `createAuditDelivery`. Go exposes `NewAuditDelivery`. The product runs the delivery call explicitly for each product, environment and sink. Configure an HTTPS endpoint, a bearer credential and the same sink ID used by the audit writer. The worker needs read access to the retained audit tables and permission to update only the delivery state, attempts, next attempt and delivered timestamp columns.

```mermaid
sequenceDiagram
  participant W as Product scheduler
  participant S as Heimdall SDK
  participant D as PostgreSQL audit outbox
  participant C as SIEM protocol receiver
  W->>S: Deliver a bounded page
  S->>D: Read and claim due event IDs
  S->>C: Send scoped authenticated JSON batch
  alt Accepted events
    C-->>S: Receipt with a subset of event IDs
    S->>D: Acknowledge only the accepted IDs
  else Permanent event rejection
    C-->>S: HTTP 422
    S->>S: Split the batch to isolate the rejected event
    S->>D: Quarantine the rejected singleton
  else Transport or receipt failure
    S->>D: Preserve pending rows for their scheduled retry
  end
```

The request includes `productId`, `environment`, `sinkId`, `eventIds` and `events`. The receiver returns the same scope and a nonempty unique subset of accepted IDs after durable acceptance. Deduplicate by product, environment and event ID. Use HTTP 422 only for a permanently invalid event in an otherwise valid batch. Other failures retain pending rows. The exporter returns counts of claimed, delivered and quarantined events, and exposes a requeue operation for a known quarantined event and attempt counter.

Claims persist retry times before transport. Delivery retries use exponential backoff with jitter. A lost response can produce duplicate delivery. The configured custom-event validator is applied when reading product events. The receiver in the PostgreSQL integration is an HTTP loopback protocol fixture. Vendor compatibility, backlog monitoring and the independent ZITADEL collector remain part of Phase 6.

PostgreSQL run `heimdall-records-1b845d4ad1e64eb69e010cf6c48daa9d` exercised both SDKs against the loopback receiver. An outage and an invalid receipt kept events pending, a permanent rejection quarantined the bad event and delivered the healthy event in the same batch, and an explicit replay delivered it. The receiver fixture does not establish compatibility with a particular vendor.

## Custom event ingestion

TypeScript `createCustomAuditWriter` and Go `NewCustomAuditWriter` bind trusted product events to a configured product, environment and set of sinks. The writer fixes the event source and accepts only registered `product.*` event types, recognized schema versions and bounded, permitted attributes. The product backend supplies the authorized actor and tenant. Use the same event payload, identifier and timestamps on retry. A different payload under the same event ID conflicts.

```mermaid
sequenceDiagram
  participant P as Product backend
  participant S as Heimdall SDK
  participant D as PostgreSQL audit outbox
  participant C as SIEM receiver
  P->>S: Record custom event with trusted actor and scope
  S->>S: Validate namespace, version and attributes
  S->>D: Commit event, receipt identity and delivery rows
  D-->>P: Durable receipt
  S->>C: Deliver registered custom event
```

Keep validators for historical versions available to the delivery worker until its backlog drains. The product owns schema registration, authorization and any transaction that must bind the event to its business data.

PostgreSQL run `heimdall-records-9acedb7368ff4033a855e7330c735820` exercised both writers and exporters. The tests rejected reserved event types, unregistered versions and sensitive attributes, confirmed stable receipts on replay, rejected changed payloads and delivered a valid custom event.
