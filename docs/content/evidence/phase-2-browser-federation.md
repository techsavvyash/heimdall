---
title: Phase 2 browser-bound federation callbacks
date: 2026-11-11
---

The pinned Railway ZITADEL and Keycloak services completed real browser SAML and OIDC logins through a disposable loopback callback harness. This proves a browser-bound intent callback can feed the previously verified intent/session/token chain. It does not implement production SDK sessions. Phase 2 remains open.

## Data flow and measured checks

```mermaid
sequenceDiagram
  participant B as Browser
  participant H as Loopback test harness
  participant I as Enterprise IdP on Railway
  participant Z as ZITADEL on Railway
  B->>H: One-time test start link
  H-->>B: HttpOnly SameSite=Lax binding cookie and redirect
  B->>I: Selected SAML or OIDC authentication
  I->>Z: Signed SAML response or OIDC callback
  Z-->>B: Intent callback redirect
  B->>H: Cookie, flow state, intent ID and credential
  H->>H: Validate binding, deadline and exact intent; claim once
  H-->>B: Redirect to token-free result page
  H->>Z: Verify intent provider, upstream subject and local subject
  H->>Z: Create session and complete downstream product OIDC via HTTP
  Z-->>H: Signed ID token and API-audience user token
  H->>H: Verify signature, audience, nonce, sid and auth_time
  H->>Z: Delete disposable sessions and organization
  H->>I: Delete disposable realm
  H-->>B: Passed only after verification and cleanup
```

Both runs reject missing/wrong browser cookies with HTTP 401, wrong flow state or intent ID with 400, and a repeated callback with 409. These negative requests are harness HTTP tests; the successful upstream login and callback run in the browser. The claim occurs synchronously before asynchronous upstream verification. Failed verification cannot turn the claimed callback into a reusable login.

Existing assertions additionally verify provider and user binding, reject invalid intent credentials and mismatched users, bind the session to signed downstream sid/auth_time, and reject intent/code reuse. The downstream product OIDC exchange is driven by backend HTTP requests, not a second browser callback. SAML assertion tamper/replay tests remain in the separate non-browser protocol suite.

## Reproduction and evidence

Use the protected environment described in deploy/verification/zitadel.md. Run tests/integration/federation.mjs with HEIMDALL_PROBE_BROWSER=true, HEIMDALL_PROBE_NARROW_LOGIN=true and HEIMDALL_PROBE_FEDERATION_PROTOCOL=oidc or saml. Do not enable the shortened intent-expiry probe simultaneously. Open the printed loopback start link and sign in using the printed disposable fixture credentials within five minutes. Treat stderr as sensitive transient test output. Successful stdout contains sanitized evidence only; credentials and callback URLs are not committed.

- OIDC run: 469960d9-b403-4935-905e-90a32b0d9fa9, tests/integration/evidence/federation-browser-oidc-2026-11-11.json.
- SAML run: e8fd0bda-4246-477d-ad0b-ffc449f69ae5, tests/integration/evidence/federation-browser-saml-2026-11-11.json.

Initial attempts exposed a missing HTTP helper in the browser branch and an expired Keycloak administrator token during cleanup. They do not count as successful evidence. Cleanup now obtains a fresh fixture administrator token; the leftover owned realm was explicitly removed before the successful SAML rerun. The OIDC artifact retains its original conservative limitation strings; the scope stated here clarifies that browser binding was measured only in this test harness.

## Remaining implementation boundaries

The callback is loopback HTTP with an in-memory claim and five-minute deadline. Production HTTPS Secure cookies, durable cross-process claims, session rotation, audit persistence, and failure recovery remain SDK work. No product session is created by this probe. A process crash loses this test transaction.

Target policy changes, tenant switching, prior-account authority rejection and the cross-directory identity product choice remain separate gates. A browser-bound intent alone does not establish workspace membership or authorize a product action. The result page carries no intent credential and uses no-store, no-referrer and a restrictive content security policy.
