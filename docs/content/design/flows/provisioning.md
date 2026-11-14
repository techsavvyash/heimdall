---
title: Product and tenant provisioning
date: 2026-11-07
---

Product registration runs in an operator command or deployment job. The resulting configuration binds a backend to one product and environment.

```mermaid
sequenceDiagram
  actor Operator
  participant Setup as Setup tooling
  participant State as Deployment state
  participant Z as ZITADEL
  participant F as OpenFGA
  Operator->>Setup: Product key, environment, clients, policy
  Setup->>State: Load or create provisioning operation
  Setup->>Z: Create or reconcile project and clients
  Z-->>Setup: Project ID, client IDs, audience
  Setup->>State: Persist completed identity step
  Setup->>F: Create or reconcile store and model
  F-->>Setup: Store ID and model ID
  Setup->>State: Persist completed authorization step
  Setup-->>Operator: Configuration manifest and secret references
```

| Data | Source and destination | Validation and persistence |
| --- | --- | --- |
| Product key and environment | Operator to setup | Stable operation key, exclusive provisioning lock |
| Redirect URIs | Product manifest to ZITADEL | Explicit allowed URLs per client |
| Policy | Versioned manifest to OpenFGA | Model validation and scenario assertions before activation |
| Resource IDs | Services to deployment state | Save returned IDs after each completed step |
| Credentials | Secret store to setup or backend | Separate setup and runtime access |

Retries reconcile known IDs. A timeout after create requires discovery or operator reconciliation before another create. Names alone may be ambiguous. The operator uses a durable PostgreSQL setup journal, separate from managed-workspace tables; concrete encoding is defined during contract implementation. Existing externally provisioned IDs should also be accepted.

## Workspace creation

The product database coordinates tenant creation and its initial owner. A unique idempotency key ensures retries refer to the same tenant.

```mermaid
sequenceDiagram
  actor User
  participant API as Product backend and SDK
  participant DB as Product database
  participant F as OpenFGA
  User->>API: Create workspace with idempotency key
  API->>API: Authenticate and authorize workspace creation
  API->>DB: Insert or load pending tenant and provisioning intent
  DB-->>API: Stable tenant ID and intended owner
  API->>F: Ensure member and owner tuples
  alt Confirmed tuples
    F-->>API: Success
    API->>DB: Mark active and complete intent
    API-->>User: Active workspace
  else Timeout or failure
    API->>DB: Keep pending, quarantine uncertain dispatch
    API-->>User: Pending status or retryable error
  end
```

```mermaid
stateDiagram-v2
  [*] --> Pending
  Pending --> Active: Owner grant confirmed
  Pending --> Pending: Safe retry or explicit uncertain-write recovery
  Pending --> Cancelled: Explicit cleanup
  Active --> Suspended: Product suspension
  Suspended --> Active: Authorized restoration
  Active --> Deleting: Delete requested
  Suspended --> Deleting: Delete requested
  Deleting --> Deleted: Data and tuple cleanup confirmed
```

The product schedules bounded recovery through its existing job mechanism. Known pre-dispatch failures can retry; uncertain dispatched writes require verified quiescence before reconciliation, not a blind request replay. In managed mode the workspace stays blocked under [[design/built-in-adapters]]. API operations reject inactive tenants before authorization. Suspension is an application gate that every entry point must honor. Deletion prevents new operations first, then removes data and relationships with retryable cleanup. Tenant IDs are never reused. See [[operations]].

[[evidence/phase-5-managed-onboarding]] shows the current managed PostgreSQL API in both SDKs and records the provider and database fault tests.
