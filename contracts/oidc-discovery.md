# Internal OIDC discovery profile

Both SDKs discover metadata only from a trusted configured issuer, by removing
one trailing slash and appending `/.well-known/openid-configuration`. The issuer
identity itself remains unchanged. There is no request/token-selected discovery,
WebFinger, redirect following, credential transmission, automatic retry or stale
metadata fallback. Production requires HTTPS; HTTP is allowed only under the
explicit loopback endpoint profile.

One read has a five-second overall deadline (or earlier caller cancellation) and
a 64 KiB decoded response limit. Only HTTP 200 and valid UTF-8 JSON with intact
Unicode strings are accepted. Malformed/unsupported metadata and dependency
failures produce redacted unavailable; cancellation and deadlines remain distinct.
Invalid trusted endpoint configuration produces invalid_argument before dispatch.

The response issuer must exactly equal configuration. JWKS, authorization, token,
introspection and revocation endpoints must all pass strict endpoint validation
and share the configured issuer origin, treating explicit default ports as
identical. This deliberately supports the selected ZITADEL deployment profile,
not every arbitrary OIDC provider topology. Separate hosted Login UI does not
change the provider's authorization endpoint origin.

Require advertised code response, authorization_code and refresh_token grants,
S256 PKCE, RS256 ID-token signing, and client_secret_basic for token,
introspection and revocation endpoints. Every capability list must contain only
strings. Other advertised algorithms/capabilities are ignored and cannot broaden
the verifier's RS256-only policy. Unknown metadata extensions are ignored.
The result contains only the exact issuer and five selected endpoint strings;
TypeScript freezes it and Go returns a value copy. Runtime construction captures
this result once; no discovery refresh silently changes an existing runtime.

This is an internal construction prerequisite, not authentication or proof of
client registration, audience ownership, credential validity or storage readiness.
Live token tests feed its JWKS URL into the existing signature verifier. Full
runtime composition and browser code exchange remain separate implementation work.

Provider reference: https://zitadel.com/docs/apis/openidoauth/endpoints
