---
title: Invitations and role changes
date: 2026-11-07
---

The product stores invitation state and delivers invitations through its existing communication mechanism. The SDK authorizes grant and revoke operations. Invitations bind a tenant, permitted role, intended recipient, expiry, and a single-use token.

```mermaid
sequenceDiagram
  actor Admin
  actor Invitee
  participant API as Product backend and SDK
  participant DB as Product and SDK PostgreSQL records
  participant F as OpenFGA
  Admin->>API: Invite recipient as approver
  API->>F: Check admin delegation permission
  F-->>API: Allow
  API->>DB: Store expiring invitation and token hash
  API-->>Invitee: Product delivers invitation link
  Invitee->>API: Accept with authenticated identity
  API->>DB: Atomically claim for verified issuer and subject
  API->>F: Recheck inviter authority under current policy
  F-->>API: Allow
  API->>DB: Reserve the workspace and journal the recipient grant
  API->>F: Ensure member and approver tuples
  F-->>API: Confirmed
  API->>DB: Complete the operation and store the receipt
  API-->>Invitee: Workspace access ready
```

The reference products bind the invitation to the recipient's ZITADEL issuer and subject. The product persists the token hash, expiry, workspace, inviter, recipient and approved roles. Its first claim must precede expiry. The same recipient can resume that claim with the same idempotency key and receive the original receipt after completion. Each product owns recipient lookup and delivery.

The managed SDK checks the inviter's current delegation at issue and again before authorizing a new acceptance dispatch. It checks the durable identity deny gate for the inviter at acceptance and verifies the recipient's current principal. The workspace revision fences competing managed membership changes between planning and reservation. If the provider write has already become uncertain, the recorded recipient grant follows [[operations]] recovery. The acceptance journal identifies the recipient as initiator, and the product invitation record links the inviter.

## Delegation rules

| Operation | Proposed authority |
| --- | --- |
| Grant or revoke viewer and approver | Active admin or owner |
| Grant or revoke admin | Active owner |
| Transfer ownership | Dedicated owner workflow with product transaction and lock |
| Remove membership | Active delegated administrator, subject to last-owner protection |
| Bootstrap first owner | Trusted tenant creation workflow |

```mermaid
sequenceDiagram
  actor Admin
  participant API as Product backend and SDK
  participant DB as Product database
  participant F as OpenFGA
  Admin->>API: Remove member
  API->>DB: Serialize membership operation for tenant and target
  API->>F: Check delegation permission
  F-->>API: Allow
  API->>F: Delete membership tuple
  F-->>API: Confirmed
  API->>DB: Record revocation and pending role cleanup
  API->>F: Remove residual role tuples
  F-->>API: Cleanup confirmed
  API->>DB: Complete operation
  API-->>Admin: Revocation confirmed
```

Every permission in the baseline intersects with membership. Once its deletion is visible to a fresh check, residual roles cannot authorize. Cleanup must finish before reactivation to avoid restoring historical roles. Concurrent grants and removals use the product's serialized mutation workflow. A timeout is an uncertain outcome that needs reconciliation.

Ownership transfer must preserve at least one owner under concurrency. OpenFGA checks and writes alone do not establish that product-wide transaction. The product serializes transfers and last-owner removals, with a durable operation record to recover from partial completion.
