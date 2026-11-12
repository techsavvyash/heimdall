---
title: Phase 3 audited maintenance authorization
date: 2026-11-12
---

The internal TypeScript and Go maintenance boundary verifies a bearer token with
the deployment's identity verifier. It derives the actor from the verified
principal and fixes the product, environment and policy version to that
deployment. The policy callback receives the exact tenant, action, target and
operation ID. A null tenant selects product-level records only.

```mermaid
sequenceDiagram
  participant Operator
  participant SDK as TS or Go maintenance boundary
  participant Policy as Trusted read-only policy
  participant DB as Durable audit and identity gate
  participant Port as Trusted maintenance port
  Operator->>SDK: Bearer and scoped operation
  SDK->>DB: Verify identity and commit authentication audit
  SDK->>DB: Commit authorization request
  SDK->>Policy: Verified actor and exact scope
  Policy-->>SDK: Allow deny or error
  SDK->>DB: Commit authorization outcome
  alt Durable allow and current identity
    SDK->>DB: Recheck local denial and freshness
    SDK->>Port: Invoke operation with bound actor and scope
  else Refused
    SDK-->>Operator: Return typed error
  end
```

The configured policy is a trusted, read-only deployment integration. The
deployment supplies the pinned model and manifest metadata. The caller cannot
supply the actor or widen the scope through mutation of the callback input.
A mismatched or missing audit receipt prevents the storage callback from running.
The last identity check catches a durable deny arriving during the decision, as
well as expired or stale identity evidence.

Product-level allow and deny events use the existing authorization request and
completion protocol. The schema exception applies only to recognized audit
maintenance actions with matching permission, a known SDK user, an audit event
target, an operation ID and a policy version. Application decisions keep their
tenant requirement.

A new maintenance authorization needs room for its own durable decision.
The v5 release reservation covers the release event after authorization.
Authentication and policy failures keep the maintenance operation closed.
Operator policy configuration, binding the authorized callback to production
maintenance commands, and scheduling remain open.

## Verification

Shared fixtures exercise allowed and denied exact scopes, invalid credentials,
audit receipt failure, policy outage, revocation and stale identity. The live
profile uses the real retained v5 append port with signed-token verification
against a controlled provider fixture.

Pinned PostgreSQL run `heimdall-capacity-backfill-7d479bc7291a46258a9b75dadf345fd7`
passed the live authorization profile in both languages. It verified ten completed
product-level decisions in the durable v5 ledger. The shared tests confirm
that missing or mismatched receipts, denial, revocation and stale identity keep
the callback from running. Workspace checks passed with 1,729 TypeScript tests,
125 environment-dependent skips and Go race tests. The documentation check
parsed 197 Mermaid diagrams.
