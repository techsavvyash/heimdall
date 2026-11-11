# Internal audited authentication

The TypeScript AuditedAuthentication and Go auditflow.Authentication compositions
construct their own identity verifier from one captured product/environment/policy
configuration, credential loader and durable identity gate. Request-level inputs
are only the raw token, trusted session context and cancellation. Session context
must come from the validated session store; bearer mode uses the literal bearer.
No caller-supplied actor, product, outcome or event attributes enter audit records.

Each invocation, including a cache hit, creates a fresh authentication.completed
event and request ID. Successful identity evidence supplies the user actor; a
verification failure uses unknown actor rather than copying untrusted claims.
Target is the configured product and tenant is null because this operation does
not choose a workspace. Source/sdk version/policy are fixed SDK metadata. Tokens,
cookies, token digests, session keys, credential values and error messages/stacks
are never copied to the event.

The identity operation has a five-second total budget and respects earlier caller
cancellation. Known failures map to deny (unauthenticated/forbidden), invalid,
cancelled or unavailable; outcome_unknown from a dispatched invalidation retains
its returned error while the authentication observation is unavailable. Arbitrary
errors become unavailable. This does not undo or retry an identity invalidation.

Before returning any identity result or failure, append the immutable event and
validate the durable receipt's product/environment, event ID, timestamp and digest.
The append has an independent five-second deadline so a cancelled request can still
record a cancellation. Pre-cancelled calls do not verify identity but do attempt
audit. Missing/mismatched/failed/uncertain receipts return audit_unavailable and
release no principal. The shared decision audit writer performs these same checks.
There is no in-memory delivery queue and no automatic append retry.

A committed succeeded event records the observed verification, not a guarantee
that product code received a principal or performed work. Cancellation during
commit still prevents return. After commit, expiry, backward clock movement and
freshness are checked again: expired evidence returns unauthenticated; stale or
clock-invalid evidence returns unavailable. Positive freshness uses the configured
window, default 60 seconds. Zero-cache mode performs online verification each time
and allows only the invocation's five-second observation window. A slow audit
commit cannot extend either token expiry or identity freshness. These final guards
can refuse release after a succeeded observation was committed. Before returning
the refusal, append a second completion observation with a fresh event ID, the
same request ID, and the refusal outcome/reason. It never erases the original
observation. This correction has another independent five-second append budget;
a failed correction returns audit_unavailable. No successful result follows this
path, so no revalidation loop is required. Principal provenance does not imply timeless freshness or tenant
permission. Later authorization must apply its own mandatory gates.

Normally there is one completion observation per authentication invocation, rather than
an authorization requested/completed pair. A crash before its commit releases no
principal. Public runtime construction, session expiry/cookies/refresh, audit
capacity configuration, recovery workers and enterprise policy remain separate
gates. Invalidation plus authentication audit are separate transactions; failure
to append never rolls back the conservative local denial.

Shared identity-status fixtures exercise the same outcomes through both audited
compositions. authentication-release.json defines final freshness/expiry vectors.
PostgreSQL tests compose real signed fixture tokens and provider-status fixtures
with restricted identity and audit database roles, durable session invalidation,
six authentication events and six outbox rows in each language's isolated scope.
