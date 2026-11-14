---
title: Authorization and business data flow
date: 2026-11-07
---

A protected operation combines verified identity, an active tenant, an OpenFGA decision, and a tenant-scoped database operation. Every request derives the product and model from backend configuration.

```mermaid
sequenceDiagram
  participant C as Client
  participant A as Product backend and SDK
  participant D as Product database
  participant F as OpenFGA
  C->>A: Approve invoice with tenant ID and invoice ID
  A->>A: Validate access token and permission name
  A->>D: Read active tenant and invoice by tenant ID plus invoice ID
  D-->>A: Invoice belonging to tenant, or missing
  alt Resource found and tenant active
    A->>F: Check principal, tenant, can_approve_invoice, pinned model
    alt Allowed
      F-->>A: Allow
      A->>D: Conditional update by tenant ID, invoice ID and version
      D-->>A: Updated row or conflict
      A-->>C: Success or conflict
    else Denied
      F-->>A: Deny
      A-->>C: Forbidden
    else Dependency unavailable
      F-->>A: Timeout or error
      A-->>C: Retryable availability error
    end
  else Missing resource or inactive tenant
    A-->>C: Scoped error
  end
```

| Field | Authority | Rule |
| --- | --- | --- |
| Principal | Validated identity | Ignore caller-supplied actor identifiers |
| Tenant ID | Request plus product lookup | Validate format, lifecycle, and membership through permission |
| Resource tenant | Product database | Query and mutate with tenant and resource IDs |
| Permission | Product manifest | Reject unknown values |
| Store and model | Deployment configuration | Pin on every authorization call |
| Decision | OpenFGA | Deny blocks operation, failure produces availability error |

For sensitive writes, request higher-consistency checks and avoid SDK allow caching. OpenFGA's higher-consistency option bypasses its query caches. A successful check and a product database commit remain separate operations, so revocation can race an in-flight write. See [[operations]] for the stated guarantee. [OpenFGA consistency](https://openfga.dev/docs/interacting/consistency)

## List and export flow

```mermaid
flowchart TD
  Query[Authenticated list request] --> Tenant[Resolve active tenant]
  Tenant --> Mode{Permission model}
  Mode -->|Tenant-wide permission| Check[Check tenant read permission]
  Check --> Rows[Query rows with tenant predicate]
  Mode -->|Per-resource permissions| Candidates[Fetch tenant-scoped candidates]
  Candidates --> Batch[Check candidate resource permissions]
  Batch --> Filter[Collect allowed rows with bounded pagination]
  Rows --> Response[Return permitted data]
  Filter --> Response
```

Tenant-wide RBAC permits normal database pagination after the tenant permission check. Per-resource access needs an authorization-aware listing contract, including filtering, ordering, total counts, and work limits. Filtering only one database page can produce short pages and incorrect counts. That extension needs its own acceptance tests before release. Exports and background jobs must apply the same scope.
