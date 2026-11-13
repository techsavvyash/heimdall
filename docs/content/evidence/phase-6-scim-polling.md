---
title: Phase 6 SCIM polling checkpoints
date: 2026-11-14
---

The TypeScript PostgreSQL entry point exposes `createDirectoryPolling`. The Go PostgreSQL package exposes `NewDirectoryPolling`. The product schedules discovery and known-subject observations explicitly for each configured directory. Install directory migration `0004.sql` after the first three migrations. The worker needs select and insert access to the subject registry and select, insert and update access to the polling table.

```mermaid
sequenceDiagram
  participant W as Product scheduler
  participant S as Heimdall SDK
  participant D as Product PostgreSQL
  participant Z as ZITADEL
  W->>S: Discover a bounded page
  S->>D: Read directory checkpoint and revision
  S->>Z: List the requested SCIM page
  Z-->>S: User IDs and directory provenance
  S->>D: Record subjects and advance checkpoint together
  W->>S: Observe a bounded page of known subjects
  S->>D: Read tracked subjects
  loop Each tracked subject
    S->>Z: Verify current identity state
    S->>D: Persist denial and pending cleanup for inactive or deleted subjects
  end
```

`discover` and `Discover` store subject IDs with provisioning-domain evidence and advance the page offset in one transaction. The revision prevents two concurrent workers from advancing the same checkpoint. Discovery restarts at the beginning after reaching the end. The provider's offset listing can skip or repeat subjects during concurrent directory changes, so the scheduler repeats complete scans. A missing list entry never deactivates a user.

`observeKnown` and `ObserveKnown` read tracked identities individually, including those absent from the current list. The scheduler can continue a pass with the returned subject cursor and restart from the beginning for the next pass. If a read or write fails, the page returns an error and a retry can repeat its completed observations safely. The subject registry records who to check. Active users still require explicit enrollment rules before any grant is made.

PostgreSQL run `heimdall-records-7c01cae43d3a48fe9166ac4b790ee32b` exercised restart from a saved offset, an upstream page failure followed by a retry, scan wraparound, deduplication and discovery followed by durable deactivation in both SDKs. End-to-end scheduler latency, enrollment, reactivation, grant provenance and SIEM remain part of Phase 6.

## Explicit enrollment

The TypeScript PostgreSQL adapter exposes `createDirectoryEnrollment`, and Go exposes `NewDirectoryEnrollment`. Install directory migration `0005.sql` and grant the enrollment worker select, insert and update access to the enrollment table. The product supplies a previously approved rule ID, workspace and roles and an authorized administrator from the product runtime.

```mermaid
sequenceDiagram
  participant P as Product administrator
  participant S as Heimdall SDK
  participant Z as ZITADEL
  participant D as PostgreSQL
  participant M as Managed membership API
  P->>S: Enroll a subject using an approved rule
  S->>Z: Verify active subject and directory provenance
  S->>D: Check local denial and bind enrollment intent
  S->>M: Add membership under a stable idempotency key
  M-->>S: Confirmed or recovery required
  S->>D: Record confirmed operation on the rule
```

The adapter checks the exact active user and provisioning-domain external ID. The product stores the enrollment policy and determines when to call the adapter. A local identity denial blocks a new enrollment attempt. The audited managed add operation checks administrator delegation and rejects the owner role. A second rule cannot claim an existing membership. Retrying the same plan uses the original operation and records its ID after confirmation.

This implements initial enrollment for a new membership. Applying directory roles to existing members, reconciling overlapping grant sources, selective revocation, reactivation and unattended scheduling remain Phase 6 work. A recorded rule identifies what the enrollment operation granted at the time. Later membership changes can alter the user's current roles.

PostgreSQL run `heimdall-records-b131c24daa464b13914279eaa4c9177b` exercised both public SDKs. The integration tests confirmed enrollment, checked the stored operation ID, retried the same request and rejected a changed role plan or an attempt by another rule to adopt the membership.
