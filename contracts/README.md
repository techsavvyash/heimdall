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

The authenticated-encryption envelope, cursor signature format, key rotation and
cross-language cryptographic vectors remain Phase 1 work. Storage CAS, callback
consumption and refresh/logout concurrency are later persistence/runtime work.

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

Receipt schemas validate shape only. Payload digest canonicalization, atomic
append/idempotency, durable acknowledgement, trusted registry loading and SIEM
replay remain implementation gates. These validators issue no durable receipts.
