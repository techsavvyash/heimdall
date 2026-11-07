---
title: Architecture and trust
date: 2026-11-07
---

The agreed deployment consists of ZITADEL and OpenFGA, supported by their databases. Heimdall ships language packages and setup tooling. A product installs the runtime package inside its backend and supplies configuration and service credentials.

```mermaid
flowchart TB
  subgraph External[Public clients]
    Browser[Browser or mobile client]
  end
  subgraph Products[Trusted product backends]
    Ledger[Ledger API and SDK]
    Support[Support API and SDK]
    LDB[(Ledger data)]
    SDB[(Support data)]
    Ledger -->|tenant-scoped queries| LDB
    Support -->|tenant-scoped queries| SDB
  end
  subgraph Auth[Shared auth deployment]
    Z[ZITADEL]
    ZDB[(Identity database)]
    F[OpenFGA]
    FDB[(Authorization database)]
    Z --> ZDB
    F --> FDB
  end
  Browser -->|OIDC login| Z
  Browser -->|access token and request| Ledger
  Browser -->|access token and request| Support
  Ledger -->|discovery and token validation| Z
  Support -->|discovery and token validation| Z
  Ledger -->|checks and authorized tuple writes| F
  Support -->|checks and authorized tuple writes| F
```

## Ownership

| Component | Owns | Product integration |
| --- | --- | --- |
| ZITADEL | Identities, authentication, identity federation, sessions | OIDC client and access-token validation |
| OpenFGA | Authorization models and relationship tuples | SDK checks, role reads, and membership writes |
| Heimdall SDK | Conventions, validation, policy mapping, error handling | In-process library with framework adapters |
| Product | Tenant lifecycle, invitations, business data, audit storage | Calls SDK and scopes database operations |
| Setup tooling | Provisioning workflow and generated configuration | Operator command or CI job |

## Credential boundary

Clients receive public OIDC configuration and their own session or token. Product backends receive OpenFGA credentials. Setup tooling receives credentials capable of creating ZITADEL projects and OpenFGA stores.

```mermaid
flowchart LR
  Operator[Operator or CI] -->|provisioning credentials| Setup[Setup tooling]
  Setup -->|project and client creation| Z[ZITADEL]
  Setup -->|store and model creation| F[OpenFGA]
  Setup -->|IDs and policy manifest| Config[Product configuration]
  Secrets[Secret manager or environment] -->|runtime credentials| SDK[Backend SDK]
  Config --> SDK
  SDK -->|configured store and model| F
```

The initial proposal trusts the product backends operated on this deployment. A store ID partitions authorization data, while service access control determines what a credential can read or write. Verify the selected OpenFGA version and its access-control configuration before promising credential isolation between products. Standard shared-key authentication alone is insufficient evidence of per-store isolation. [OpenFGA configuration](https://openfga.dev/docs/getting-started/setup-openfga/configure-openfga)

A compromised backend can exercise the privileges of its service credential. This is an explicit deployment trust assumption. See [[decisions]] for the isolation question and [[operations]] for concurrency limits.
