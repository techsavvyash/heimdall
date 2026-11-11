# Internal token-verification profile

The TS and Go verifiers implement the same RS256-only profile. This is signature
verification, not complete authentication or permission to skip online identity
status, session-policy or durable-audit gates.

Configuration is immutable per verifier: exact trusted issuer and intended product
audience, plus a same-origin JWKS URL validated with the existing production or
explicit loopback endpoint profile. Never derive URLs or algorithms from a token.
Discovery/setup integration must pin these values before constructing the verifier.

Compact JWTs are bounded to 16 KiB. Require a printable ASCII kid of 1–128 bytes
and RS256; reject critical extensions and b64 overrides. Ignore untrusted jku.
Require issuer, audience, subject, exp and iat. Numeric dates are nonnegative whole
seconds, with exp convertible to a JavaScript-safe millisecond integer. iat cannot
be in the future or at/after exp. exp must be strictly after now. Optional nbf must
be a nonnegative whole second no later than now. Clock tolerance is zero. Subject
must pass canonical identity constraints; never repair malformed Unicode into a
different subject. Audience lists may include the intended audience alongside the
provider API audience; another product audience alone never suffices.

JWKS reads have a five-second deadline, a 256 KiB response bound and no redirects.
Accept 1–16 unique public RSA keys with 2048–8192-bit odd moduli and odd exponents
from 3 through 2147483647. Optional alg/use/key_ops must be RS256/sig/[verify].
Reject private material, ambiguous duplicate kids and noncanonical RSA integers.
Reject unsupported key sets rather than silently broadening algorithm trust.

Cache lifetime is five minutes from retrieval start, with thirty seconds between
refresh starts including failures. Concurrent callers share one retrieval. A
cancelled caller does not abort that shared retrieval; it remains independently
bounded to five seconds. An unknown key after successful retrieval is
unauthenticated. A failed/expired/unavailable key set is unavailable, including
unknown-key requests during failure cooldown. Known cached keys remain usable only
within their original lifetime. A backward wall-clock movement invalidates cached
use. No signature result extends an identity-status freshness deadline.

Returned token evidence is tied to its verifier instance and cannot be fabricated
by constructing a lookalike object. It exposes issuer, subject and expiry, but no
raw token. It is not yet a public human principal. Test vectors are synthetic,
include only public keys and are never credentials for the verification stack.
