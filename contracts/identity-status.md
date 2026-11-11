# Internal human-identity freshness contract

The TS and Go IdentityVerifier composes signature verification, product-client
introspection, the authenticated user's ZITADEL user lookup and a mandatory local
identity/session gate. It is internal; public session/authorization factories and
required durable audit composition remain pending. `owns` proves origin from a
verifier instance, not permission to reuse a principal after its request/freshness.

Configuration fixes product, environment, policy version, issuer, audience, JWKS
URL and client ID. The secret loader supplies the product client's introspection
secret with cancellation. Client ID and secret are form-encoded separately before
Basic encoding. Credentials and raw tokens never appear in returned principals or
errors. No admin PAT or arbitrary privileged identity lookup is used here.

Every call verifies the signed token and checks the local deny gate, including
cache hits. Positive status caching defaults to 60,000 ms, configurable from zero
to that maximum, capped by the earlier signed/introspected token expiry. Freshness
starts before the online check, not after response arrival. At the exact deadline,
revalidate or fail; never fall back to a stale positive during outage. A newly
completed online check can authorize its current bounded call even with freshness
zero; zero never inserts a positive cache entry. The whole verification call has
a five-second deadline, bounded by the caller's earlier deadline.

Cache keys bind immutable product/environment/policy, canonical issuer/subject,
SHA-256 of the exact token and trusted request/session context. Cache capacity is
1,024 positive entries. Eviction only causes new online checks. Context is supplied
by SDK orchestration, never trusted as an arbitrary browser-provided session ID;
for sessions it must distinguish the loaded record/revision. Factories must bind
invalidation to that verified session identity/revision to prevent deleting a
replacement session. Token-only callers use the bearer context.

Introspection POST uses the configured issuer's /oauth/v2/introspect. A successful
active response must match issuer, subject and intended audience and have a valid
expiry no later than the signed expiry. Missing/malformed fields and dependency,
client-authentication or rate-limit failures are unavailable, not authorization
denials. Responses are limited to 64 KiB; redirects are refused. No automatic
transport retry occurs.

An active token must then read /v2/users/{encoded-subject} using its own bearer
credential. Setup/login therefore requests both the product audience and ZITADEL
API audience scope `urn:zitadel:iam:org:project:id:zitadel:aud`. The returned user ID
must match exactly and expose a human, not a machine. Unknown states and 403
configuration/permission failures are unavailable. No email or organization name
is accepted as identity proof.

Negative handling is deliberately distinct:

- Inactive/expired introspection, self-user HTTP 401/404 or a machine identity:
  token_inactive. Invalidate the affected token/session, without asserting the
  account was deleted or disabled. A self-user 404 is not enough to persist an
  identity-deletion tombstone.
- An explicitly returned human USER_STATE_INACTIVE/LOCKED: identity_inactive.
- An explicitly returned human USER_STATE_DELETED: identity_deleted.

Before returning unauthenticated, await the mandatory invalidation gate. Clear
matching cached positives and mark matching in-flight observations negative so a
late positive cannot restore access/cache state. Token negatives affect that token
across contexts; identity negatives affect all that identity's token observations
in the instance. Another independently valid token is not banned by a token-only
negative. A second local deny read precedes publishing fresh positive evidence.

The built-in factory must provide a primary-database deny check and durable scoped
session invalidation; core consumers provide equivalent durable behavior. Gate
failures never release a principal. If cancellation wins after dispatching the
invalidation callback, its outcome is unknown and safe idempotent reconciliation
is required. Supplied callbacks must honor cancellation; a deadline bounds the
caller's wait but cannot forcibly terminate arbitrary Go/JS callback code. There
are no implicit workers, timers for reconciliation or schema migrations.

Provider-wide disable/delete propagation, strict enterprise evidence, reactivation
cleanup/current grants, refresh/logout orchestration, public factories and audit
composition remain separate required release gates. A positive provider response
never clears a local denial. Already-completed authentication may remain in flight;
this does not fence business transactions after a later revocation.
