# Shared SDK contracts

Language-neutral contracts and fixtures define matching TypeScript/Go behavior.
Version 1 is under construction as Phase 1, not yet a complete released contract.

## Canonical human identity encoding v1

Input is an issuer/subject pair of nonempty Unicode scalar strings. Issuer is at
most 2048 UTF-8 bytes; subject at most 1024. Reject ill-formed Unicode. Preserve
strings exactly: no case folding, Unicode/URL normalization or trimming.
Issuer trust and token verification are separate runtime responsibilities;
encoding a pair does not create a verified principal.

Encode the compact JSON array of issuer and subject in ASCII. Escape quote and
backslash, use JSON short escapes for backspace/formfeed/newline/return/tab,
and lowercase Unicode escapes for remaining controls and every UTF-16 code unit
at or above U+007F. Supplementary scalars use their surrogate pair. Do not escape
slash, less-than, greater-than or ampersand. Prefix unpadded base64url with user:.

Fixtures contain inputs and exact expected IDs or invalid_argument. Both runners
also reject native malformed string representations: unpaired TS surrogates and
invalid Go UTF-8. Neither runtime silently repairs malformed input.

Changing encoding, scope interpretation or error meanings requires a new contract
version and migration review. Additive changes preserve existing vectors.

## Errors and cancellation v1

The error schema and code corpus are shared by both runners. Errors serialize
only a known code and optional opaque operationId; SDK-generated messages use the
code and never retain upstream exception text or arbitrary abort reasons.
An empty operation ID means absent in internal constructors; wire objects omit it.
Nonempty operation IDs use 1–128 ASCII letters, digits, hyphens or underscores.

Transport classification applies only when no definitive remote outcome is
available. Before mutation dispatch, dependency failure, deadline and cancellation
remain distinct. Once a mutation may execute, all three classify as outcome_unknown
and retain its operation ID for recovery. This classification does not implement
the journal or prove that a write has stopped. A confirmed service rejection is
mapped separately in the future integration.

A successful denied permission check returns false, not an unavailable error.
Actual permission evaluation and audit persistence are later runtime work; no
permission-check stub is introduced to manufacture evidence now.

## Schema conformance

The test runners automatically compile every root *.schema.json and require a
matching positive/negative corpus in fixtures/schemas/. They use JSON Schema
2020-12 with no coercion or inserted defaults. Ajv 8.20.0 (released April 24, 2026) and Go jsonschema/v6 v6.0.2 (released May 23, 2025) satisfy the minimum
dependency age. Both are test dependencies at this stage.

Configuration v1 defines the production binding shape: product/environment,
issuer, audience, client/project, OpenFGA endpoint/store/model and manifest
version. It contains no inline secrets or per-request overrides. This schema is
structural validation, not proof of issuer trust, URL reachability, service ID
existence or manifest compatibility. The URL pattern rejects insecure endpoints,
userinfo, whitespace, query and fragment; complete endpoint parsing and verified
service bindings remain runtime validation work. An explicit loopback development
profile remains to be specified before local runtime configuration ships.

Specification references:

- https://ajv.js.org/json-schema.html
- https://github.com/santhosh-tekuri/jsonschema/tree/v6.0.2

## Product manifest v1

The manifest declares schemaVersion, productId, positive version, clients,
permissions, roles, ownerRole and delegation. Roles use lowercase ASCII names
starting with a letter and containing letters/digits/underscores (maximum 64).
Permissions are dot-separated lowercase identifiers (maximum 128). These are
product policy names, not user-supplied OpenFGA relation expressions.

Each role explicitly lists its business permissions. The selected ownerRole must
exist, but does not silently acquire every permission. Bootstrap and ownership
transfer recognize that role separately. Delegation maps an acting role to the
non-owner roles it may administer; missing or empty entries grant no delegation.
Every referenced actor, target and permission must be declared. No delegation
entry can target ownerRole, even when the actor is the owner. The runtime must
also verify active membership and the actor's current roles before mutation.

The structural schema rejects duplicates, unsupported fields and malformed
identifiers. Internal semantic validators run after structural validation and
reject dangling references and mismatched config productId, manifestVersion or
clientKey. Tests prove both the structural precondition and semantic result.
A config now includes clientKey to identify the manifest client that was
provisioned; clientId remains the external identity-service client ID.

Clients currently declare production HTTPS redirect URIs. The manifest is
operator-owned configuration, never tenant-submitted policy. Pin it with the
deployed model. Changing permissions or delegation requires a new manifest
version and the model/config migration workflow. Semantic validation alone
neither provisions a model nor authorizes a live user.

## Session and cursor payload v1

Timestamps are integer Unix milliseconds in the JavaScript safe-integer range.
Both languages compare exact expiry with now >= expiresAt. Session payloads
carry scope, issuer/subject, policy version, access/refresh tokens, activity and
absolute/idle/token deadlines, identity-validation time, and a state of active,
refreshing or reauth_required. A refreshing state requires an attempt ID and
deadline; other states prohibit that field. These are plaintext before
encryption, never cookie or log contents.

The internal session status helper requires a structurally validated and decrypted
record plus trusted expected scope. It validates timestamp ordering, rejects
wrong scope/policy and expired sessions, and distinguishes active, refreshing,
refresh_required, identity_revalidation_required and reauth_required. A refresh
attempt reaching its deadline requires reauthentication, not token replay.
Freshness is supplied explicitly between zero and 60,000 milliseconds; the runtime
default is 60,000. Zero requires an authoritative check on every request.
An active status is only a record-state result, not verified authentication or
permission to skip token, identity or enterprise-policy validation.

Cursor payloads bind format/kind, product, environment, principal, model, last
consumed source position, issue time and expiry. The internal scope helper runs
only after structural and signature verification. Wrong scope, future issue time
and exact expiry return invalid_argument. Payload schemas and scope checks alone
do not authenticate a cursor.

The following section defines authenticated envelopes and cross-language vectors.
Storage CAS, operational key rotation, callback consumption and refresh/logout
concurrency are later persistence/runtime work.

## Cryptographic envelopes v1

Internal RecordCipher uses AES-256-GCM via node:crypto and Go crypto/aes/cipher.
The wire string is hr1.keyId.base64url(nonce || ciphertext || tag), with a random
12-byte nonce and 16-byte authentication tag. Go uses NewGCMWithRandomNonce;
Node uses randomBytes and a fixed 16-byte auth tag. Callers cannot supply nonces.
No partial plaintext is returned before authentication succeeds.

Associated data is ASCII, pipe-separated in this exact order:
heimdall.record.v1, keyId, productId, environment, record kind, recordKey.
Kinds are session/login. Scope and key fields use bounded ASCII letters, digits,
underscore and hyphen, so separators cannot be injected. The recordKey is the
storage identifier/hash, never the raw browser cookie. Plaintext is limited to
1 MiB, and must subsequently pass its payload schema and semantic validation.

CursorSigner uses HMAC-SHA256 with a separate signing key set. Its wire string is
hc1.keyId.base64url(payload).base64url(mac). The MAC input is ASCII
heimdall.cursor.v1|keyId|base64url(payload). Payload is limited to 64 KiB.
Signing does not encrypt the payload. Consumers treat it as opaque; do not put
secrets in it. Verify the MAC before parsing its JSON and checking schema, scope,
model, principal and expiry. Authentication of bytes does not imply payload validity.

Both formats require canonical unpadded base64url, reject whitespace/padding,
unknown versions and unknown keys, and bound input size before decoding. Key
configuration requires 32-byte keys, 1–64-character ASCII key IDs, a present
active key and at most 32 retained keys. Constructors copy key bytes. Readers
accept retained keys; writers use only the active key. Key IDs are authenticated.

Keys must be random secret material, stable across serving replicas and distinct
between record encryption and cursor signing. Retain retired decryption keys until
records expire or are rewritten; retain signing keys through cursor lifetime.
Configuration/rotation orchestration is later adapter work. In particular,
Go's GCM API requires fewer than 2^32 encryptions per key across all replicas.
A durable key-usage/rotation policy is a production release requirement; these
internal primitives do not claim to enforce a deployment-wide counter.

All cryptographic fixtures use public synthetic keys and payloads. The shared
corpus includes a reference vector and captured outputs from both SDK writers,
plus tampering, mismatched contexts and noncanonical base64. Both languages read
both writers' outputs. No fixture key is suitable for deployment.

References:

- https://nodejs.org/docs/latest-v24.x/api/crypto.html
- https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce
- https://pkg.go.dev/crypto/hmac#Equal

## Operation intent, journal and retry contract v1

The operation-intent schema covers create_workspace, add_member, remove_member,
assign_roles and transfer_owner. The initiating actor and target use issuer/subject
pairs. Creation requires target=actor, a nonblank display name, no role list, and an
empty request workspaceId. The actual immutable workspace ID is allocated once in
the journal transaction, outside the creation request fingerprint. Other operations
require a workspace ID and empty name. Removal/transfer prohibit a role list.
Product policy must separately validate roles and delegation before recording intent.

Display names reject ASCII controls and ill-formed Unicode; blank Unicode
whitespace (including BOM) is rejected for creation. Inputs are not normalized.
Idempotency keys are nonempty Unicode strings bounded to 256 UTF-8 bytes. They
are caller correlation keys, never authentication credentials.

Fingerprint input is the compact canonical ASCII JSON array:
[heimdall.intent.v1, productId, environment, kind, encodedActor, encodedTarget,
workspaceId, name, sortedRoles, manifestVersion].
String escaping follows identity v1. Roles are schema-validated ASCII identifiers,
sorted without modifying the request. Fingerprints are lowercase SHA-256 hex.

The idempotency digest uses:
[heimdall.idempotency.v1, productId, environment, kind, encodedActor, rawKey].
Thus changed payload conflicts within one scoped key; a different caller cannot
use that key to inspect another actor's operation. Digests are checksums, not
authorization or proof of remote execution.

The journal schema stores immutable intent, both digests, allocated workspace ID,
operation ID, generation, dispatch count, state and timestamps. Required storage
invariants: recompute/verify fingerprints; bind non-create request workspace ID to
journal workspace ID; createdAt <= updatedAt; never change intent/digests; preserve
terminal keys/tombstones; atomically enforce unique idempotency scope. These
cross-record/transaction invariants are persistence implementation gates.

The internal transition function proposes a next state, never commits it. Storage
must compare-and-swap the expected generation and persist the dispatch marker,
incremented dispatch count and local workspace block before sending a mutation.
A worker cannot progress a stale generation. Cancellation is allowed only before
any dispatch. Confirm requires a definitive remote result and verified postconditions;
complete requires durable confirmation and the local completion transaction.
A definitive rejection can become retryable only when the adapter proves no
mutation occurred. Transport failures do not qualify.

Uncertain has no automatic outgoing transition. Lease expiry, a relationship read,
or a stopped worker cannot clear it. Operator recovery must establish real
quiescence and reconcile under the eventual verified recovery protocol. This pure
state helper does not fence remote requests or provide a force-unblock API.

## Audit envelope v1

Audit event, durable receipt and delivery receipt schemas define strict wire shapes.
Structural validation precedes semantic validation. Reserved event types/outcomes
are enumerated in audit-types-v1.json. Authorization requires decision/model/manifest
IDs; allow/deny additionally require tenant and permission. Early invalid requests
may have unknown actors/tenants. Authentication success and authorization allow
require a user. Membership/ownership require operation, tenant, model and manifest.
System audit actors identify trusted scheduled workers, not public service principals.

Custom events require registered product.* types/versions and attribute schemas.
Trusted call context prevents custom producers impersonating reserved SDK types.
Backend code must derive actor/scope from verified context; validation cannot prove
metadata truth, persistence or delivery.

Attribute root depth is zero; maximum depth 8, container members 32, ASCII token
key length 64 and string size 4096 UTF-8 bytes. Finite numeric magnitude is bounded
by the safe-integer maximum. Structural weight is capped at 32768: null/bool=1,
number=9, string=1+UTF-8 bytes, container=1+children plus object key bytes.
This is not a serialized event-byte limit. Known secret/reserved metadata keys are
rejected recursively, ignoring case, underscores and hyphens. Secrets concealed
in innocuous values still require trusted schemas and sanitized construction.

Receipt schemas validate shape; the receipt helpers below check scope and payload
binding. Atomic append/idempotency, durable acknowledgement, trusted registry
loading and SIEM replay remain runtime gates. These validators issue no receipts.

### Audit payload fingerprint and receipt binding

The fingerprint is lowercase SHA-256 over ASCII heimdall.audit.v1 followed by a
zero byte and the canonical typed value encoding of the complete validated event.
This is a private v1 fingerprint encoding, not JSON serialization or JCS. It avoids
cross-language differences in JSON whitespace, key order and decimal formatting.
The stored/exported event remains ordinary JSON.

Value tags are single ASCII bytes: n=null, t=true, f=false, d=number, s=string,
a=array, o=object. A number carries 8-byte big-endian IEEE-754 binary64; negative
zero becomes positive zero. Only finite magnitudes <= 9007199254740991 are allowed.
A string carries a 4-byte unsigned big-endian UTF-8 byte count then exact UTF-8;
ill-formed Unicode is rejected and text is never normalized. Arrays carry a 4-byte
item count followed by values in order. Objects carry a 4-byte property count,
then key-string/value pairs sorted by ASCII key bytes. Keys must be ASCII tokens
of 1–64 characters, matching validated envelope/attribute keys. Missing optional
fields differ from present null fields. No fields, including recordedAt, are omitted.

The encoder rejects more than 1 MiB of encoded value bytes and nesting deeper than
16 (root zero). Envelope and attribute validation impose narrower input limits.
Use JSON-decoded numeric values in Go (float64); semantic integers must first pass
the schema's safe-integer bounds. Reject duplicate JSON keys at a raw ingestion
boundary before parsing, rather than silently accepting conflicting field values.

Create the immutable event once, including event ID and recordedAt, before the
first append attempt. Retries reuse it unchanged; never restamp recordedAt. Storage
atomically enforces unique (productId, environment, eventId): identical digest
returns the original receipt, different digest conflicts. This does not establish
business-operation idempotency or authenticate an event.

A local receipt must exactly match product, environment, event ID, recordedAt and
payload digest. Mismatch returns audit_unavailable and cannot release protected
work. A valid receipt still requires the store's proven commit-before-reply behavior.
SIEM receipts must match sink/product/environment and acknowledge a nonempty,
duplicate-free subset of submitted IDs. Reordering is allowed; unknown IDs, empty
or duplicate acknowledgements fail with unavailable. Invalid responses advance
nothing. Valid partial responses advance only those IDs, preserving other events
for retry. Deduplication uses product/environment/event ID, never event ID alone.
Authenticated transport, scope filtering and durable checkpoints remain runtime work.

## Login payload and managed workspace gate v1

Login payloads use the existing encrypted record envelope with kind=login. The
storage key/AAD bind the opaque state lookup to product/environment. The encrypted
payload contains issuer/client/callback/allowlisted return-destination key, nonce,
PKCE verifier, browser-binding digest, timestamps, claim state and optional tenant
policy. No login secret belongs in audit, discovery or ordinary exports.

The browser binding is SHA-256 of the exact ASCII opaque browser cookie, whose
runtime generator must use at least 32 random bytes and unpadded base64url. Store
only the lowercase digest in the encrypted login payload. Callback code derives
the comparison digest from the presented cookie, never from a browser-supplied
hash. The internal validator compares fixed-size valid digests in constant time.
State and nonce are independently random; PKCE uses S256. Random generation,
callback parsing and OIDC verification remain runtime implementation work.

Login records have a maximum lifetime of ten minutes (the selected v1 default),
with shorter lifetimes allowed. Expiry is exclusive: now >= expiresAt rejects.
Callback validation matches exact trusted product/environment/issuer/client and
callback URL, checks the return key against startup configuration, and re-resolves
tenant policy. Tenant ID, organization ID, policy version and strict-SSO flag must
all still match. Added/removed policies also invalidate pending login. This is
policy binding, not proof that the identity provider actually used required SSO.

After successful validation, atomically CAS pending to claimed with claimedAt
before exchanging the code. Losing the CAS, an already claimed record or an
uncertain exchange requires a fresh login; never reset claimed to pending. Keep
the claim through expiry/cleanup. Browser binding must be checked before claim so
a stolen state alone cannot consume another browser's pending login. The pure
validator performs no database CAS or code exchange. Invalid time ordering returns
invalid_argument; expiry, replay or binding changes return unauthenticated.

Workspace records carry product/environment, immutable ID, display name, lifecycle,
mutationBlocked, non-repeating revision and creation/update times. Only active and
unblocked yields check_relationships. Pending, suspended, tombstoned, blocked and
absent records yield deny. This result never grants access: verified identity,
enterprise lifecycle/policy checks, fresh membership-gated OpenFGA evaluation and
durable audit remain mandatory. Database failures propagate unavailable; never
convert a failed read into an absent record. A wrong-scope database result also
returns unavailable, rather than evaluating it as a different tenant.

Read authoritative lifecycle from the primary database on every managed check.
Callers must audit local denials before reporting a completed decision. This check
does not fence an in-flight request or justify clearing a mutation block. Revision
uniqueness, atomic transitions, suspension/resumption authorization and tombstone
retention are persistence/runtime gates. Schema validation cannot prove randomness,
immutability, name validation at creation or primary-database consistency.
