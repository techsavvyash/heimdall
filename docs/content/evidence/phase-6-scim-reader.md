---
title: Phase 6 SCIM directory reads
date: 2026-11-14
---

The TypeScript provisioning entry point exposes `createDirectoryReader`. The Go provisioning package exposes `NewDirectoryReader`. Each reader is bound to a server-configured ZITADEL issuer, organization and read credential with the directory's provisioning-domain metadata. The credential used in the reference probe is separate from the SCIM write credential. See [[phase-2-scim-credentials]].

```mermaid
sequenceDiagram
  participant W as Product lifecycle worker
  participant S as Heimdall SDK
  participant C as ZITADEL SCIM Users
  participant U as ZITADEL Users API
  W->>S: Request a bounded discovery page
  S->>C: Read the configured organization
  C-->>S: Subject IDs and scoped external IDs
  S-->>W: Candidates and next page position
  W->>S: Read an exact subject for reconciliation
  S->>U: Verify user ID, organization and current state
  alt Active user
    S->>C: Read the exact SCIM resource
    C-->>S: Current active flag and directory provenance
  else User API returns 404
    S->>C: Check directory access and exact subject
    C-->>S: Directory readable and subject absent, or error
  end
  S-->>W: Verified state or typed error
```

Discovery pages use the provider's offset pagination. The scan can miss or repeat a subject when the directory changes between pages. Use repeated bounded scans for new-user discovery, deduplicate by issuer and subject, and re-read known identities independently for deactivation and deletion. Page membership and external IDs are discovery inputs. Enrollment requires an exact current read, directory provenance and an explicit product rule. Persist the identity under issuer and subject.

The exact read verifies the organization and human user type. For active users, it also checks the exact SCIM resource and its active flag. An active result without a directory external ID has no enrollment provenance. For a user API 404, the reader requires successful directory access and an exact SCIM 404 before returning `missing`. Use that state for a previously tracked identity whose organization is already established. Disagreeing APIs and unexpected responses return a typed failure.

## Verification

Shared fixture `directory-reader-v1.json` runs in both SDKs. It covers active users with and without provenance, inactive and locked users, initial state, unknown states, mismatched organization and subject, ambiguous 404s, and disagreement between SCIM and user API state.

Railway verification run `e2460d59-d43d-4739-ae02-dcc9c465bbc9` exercised both SDKs against ZITADEL with a read-only organization viewer. Both read the active user and its scoped external ID, then observed the SCIM deactivation and the later deletion. The run also verified a nonempty discovery page and the empty page after deletion. It removed the disposable organizations. Reproduce with `HEIMDALL_PROBE_SCIM_SDK=true` in the protected SCIM probe environment described in [[phase-2-scim]].

This checkpoint covers provider reads. Durable ingestion, local session denial, role reconciliation and end-to-end deactivation latency remain Phase 6 acceptance work.
