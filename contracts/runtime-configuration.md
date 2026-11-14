# Internal deployment configuration loader

TypeScript parseDeployment and Go configuration.Parse accept trusted operator
configuration and manifest JSON, limited to one MiB each. They have no filesystem
or network dependencies. They enforce the config/manifest structures, required
and unknown fields, numeric/string/collection bounds, uniqueness, transport
profiles and strict endpoint rules before invoking semantic manifest validation
and the deterministic model compiler. Caller-supplied credentials do not belong
in either JSON document; unsupported secret fields are rejected with redacted
invalid_argument errors.

The shared schema fixtures remain the structural conformance baseline.
configuration-v1.json adds product/version/client/profile binding, unknown
permissions/roles, owner delegation, collection boundaries, Unicode lengths and
mutation-isolation cases. Runtime endpoint validation is deliberately stricter
than the JSON schema's basic URL pattern. No endpoint is inferred from a token,
request parameter, tenant name or unverified metadata.

JSON integer spellings such as 1e0 are accepted consistently. Go canonicalizes
validated JSON values before decoding typed integer fields. Both SDKs reject
malformed Unicode rather than repairing it into another identifier; Go shares
this check with the identity verifier. As with ordinary JSON decoding, duplicate
object keys use the last occurrence. Configuration is trusted operator input,
not an untrusted request format.

A valid result contains configuration, the bound manifest and the full compiled
model/hash/role/permission mappings. TypeScript recursively freezes the snapshot.
Go keeps its binding private and returns value copies or detached map/slice
copies; modifying an accessor result cannot mutate the captured deployment.
Canonical JSON field tags preserve config/manifest spelling on Go round trips.

This is structural and local policy validation, not remote readiness. The caller
must still verify the exact store/model with the pinned-model guard, validate
storage readiness, configure credential loaders and durable audit, and build the
identity/session runtime. Matching policy definitions do not prove that an
operator selected the intended external product/project/store when two products
share the same policy. Provisioning/configuration remains the trusted binding
for those external IDs. The loader performs no remote writes and does not expose
public authentication, authorization, login or session APIs by itself.

Audience validation is shared with the token-verifier construction boundary: both
accept 1–2048 Unicode code points without normalization, rather than imposing a
stricter byte limit after configuration loading. audience-boundaries.json supplies
signed public test vectors for accepted ASCII/Unicode limits and wrong-audience
rejection; its private signing key is not retained.
