---
title: Phase 4 cookie authentication and local logout
date: 2026-11-13
---

Both internal browser adapters read the opaque cookie, derive the storage key, and load the unexpired encrypted session. They authenticate its access token through the existing audited identity path and bind the identity check to the loaded record revision. Authentication advances idle expiry up to the absolute limit by compare-and-swap. The SDK returns the principal only after the write is confirmed.

```mermaid
sequenceDiagram
  participant R as Protected request
  participant S as SDK
  participant DB as PostgreSQL
  participant L as Logout
  R->>S: Send session cookie
  S->>DB: Load session and revision
  S->>S: Verify identity and commit authentication audit
  alt Session revision still current
    S->>DB: Update activity with compare and swap
    DB-->>S: Confirm updated record
    S-->>R: Return principal
  else Logout wins
    L->>DB: Delete session
    S->>DB: Update activity with compare and swap
    DB-->>S: No record updated
    S-->>R: Reject authentication
  end
```

Logout requires a POST with an Origin matching the configured callback origin. The SDK deletes the local record before clearing the cookie, including for repeat logout. The built-in flow leaves upstream revocation to explicit policy.

The pinned PostgreSQL integration checks successful cookie authentication, activity revision advancement, cross-origin logout refusal, cookie clearing, and logout between audited authentication and session update. Both languages reject reuse of the logged-out cookie. A competing session update can return unavailable and the caller can retry with the current revision.

Refresh exchange, public constructors, tenant login and end-to-end reference product acceptance remain Phase 4 work.
