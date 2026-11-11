# Shared SDK contracts

Language-neutral contracts and fixtures define matching TypeScript/Go behavior.
Version 1 defines the executable baseline. This is not a released SDK or proof of runtime behavior.

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

Configuration v1 defines the binding shape: product/environment,
issuer, audience, client/project, OpenFGA endpoint/store/model and manifest
version. It contains no inline secrets or per-request overrides. This schema is
structural validation, not proof of issuer trust, URL reachability, service ID
existence or manifest compatibility. Schema validation is followed by manifest
binding and endpoint semantic validation. The explicit transport profile described
below governs local HTTP; omission keeps production HTTPS requirements.

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

### Record-key usage accounting (implementation in progress)

The independent `heimdall_keys` schema counts encryptions by lowercase SHA-256
of ASCII `heimdall.record-key-usage.v1` followed by a NUL byte and the 32 raw
AES key bytes. Product, environment and display key ID are deliberately excluded.
All replicas and products sharing key material must use the same accounting
ledger. Separate databases cannot independently establish a global allowance.
`record-key-usage.json` supplies shared synthetic fingerprint and limit vectors.

A key has an immutable explicit limit from 1 through 4294967295. Its usage can
only increase, and retirement is irreversible. Runtime credentials receive
SELECT and UPDATE of `used`, never registration, deletion or policy changes.
A reservation increments usage under a row lock and commits synchronously on
the primary before encryption may start. Missing, retired or exhausted keys
return unavailable. An uncertain COMMIT returns outcome_unknown and must not
permit encryption. Failed encryption or later record persistence does not
refund a confirmed reservation. A new attempt consumes another allowance.

Registration must be deployment-owned and restricted to fresh key material;
unknown historical usage cannot safely start at zero. Restore or clone of an
older ledger requires fresh encryption keys before writes resume. Retained
keys may still decrypt existing records. Retirement prevents new reservations,
not execution under receipts already committed before retirement.

The internal schema, migration, registration, retirement and reservation functions
have disposable PostgreSQL concurrency and COMMIT-loss tests in both languages.
PostgreSQL record and identity-bound session adapters now require an explicit
accounting pool and reserve before each encryption. The pool may be the product
pool or a separate shared primary. They snapshot the plaintext before waiting,
propagate cancellation, and never encrypt after uncertain reservation commit.
Failed CAS, uniqueness conflict and identity denial do not refund allowance.
Pure envelope primitives remain unmetered internal cryptography helpers, not
production persistence entry points. Operator tooling and complete rotation/restore
orchestration remain required before release.

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

Digest helpers now perform complete intent shape/semantic validation themselves;
callers cannot bypass schema constraints by calling the digest function directly.
TS `parseIntent` and Go `ParseIntent` additionally accept a bounded JSON payload
(maximum 1 MiB), require exact camel-case fields and reject Unicode repair before
decoding. They preserve identity strings, names and role ordering; sorting is only
for the canonical fingerprint. Go's typed Intent uses explicit matching JSON tags.
JSON objects follow last-member semantics in both languages, including replacement
of a repeated identity object rather than Go struct merging. Unknown or missing
fields in the resulting object still fail. The parser never authenticates an actor.

`parseBoundIntent` / `ParseBoundIntent` recompute the fingerprint and canonical
actor/target SHA-256 hashes and compare product, environment, kind and manifest
version with indexed journal fields. Non-create workspace IDs must match the
allocated journal workspace. Create requests keep their empty workspace ID; the
separately allocated journal ID must be a valid nonempty identifier. A corrupt
payload/index pair returns unavailable, never absence. Raw input errors return
invalid_argument. These helpers do not verify OpenFGA model pins, operation state,
key-digest lookup or actor provenance; the managed persistence/API composition must
supply those checks. The raw idempotency key is not stored in the intent, so its
key digest cannot be reconstructed from that payload alone.

Shared `operation-intent-runtime.json` and `operation-intent-binding.json` cover
these runtime boundaries alongside the unchanged v1 digest vectors.

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

## Storage metadata and compatibility v1

The storage-metadata schema describes one SDK database namespace, not a tenant's
business data. Format schemaVersion is separate from databaseVersion and from SDK
package versions. Reader/writer protocol versions are integer storage-behavior
versions, not semver. All v1 version values are bounded to 1–1000; widening that
limit requires a format revision. A metadata snapshot is read consistently from
the primary database and is accepted only in ready state.

Metadata contains the applied migration ledger, database version and inclusive
reader/writer protocol ranges. The SDK bundles a trusted policy with its reader
and writer versions, inclusive tested database range and complete ordered migration
checksums through that range's maximum. Migrations are consecutive from 1, with
no duplicates, gaps or altered history. The applied ledger must contain exactly
databaseVersion entries and match the bundled checksums at every applied version.
Migration checksums are lowercase SHA-256 of the exact shared UTF-8 SQL file bytes;
commit files with LF endings and never rewrite an applied migration. Fixture
checksums are synthetic contract data, not a runnable migration catalog.

The read-only startup helper rejects malformed SDK policy as invalid_argument.
A migrating database, incompatible reader/writer range, unsupported database
version, corrupt ledger or changed checksum returns unavailable. A missing or
invalid metadata document also fails startup in the eventual storage adapter;
never infer an empty database is safe to initialize. No constructor migrates.

Rolling upgrades need an explicitly tested bridge SDK that knows the current and
next schema checksums and supports both database versions. Deploy the bridge,
apply the additive migration, then deploy the next reader/writer protocol while
both remain permitted. After draining old processes, separately remove their
compatibility and schedule destructive cleanup. Unknown future migrations are
not assumed safe just because their metadata declares compatibility. If no bridge
exists, use a maintenance window; do not bypass the check.

This helper cannot stop an already running old process. Deployment coordination
must drain incompatible readers/writers and prevent in-flight writes before any
incompatible migration. Transactional DDL and ready metadata/ledger updates must
commit together; nontransactional or interrupted work stays migrating until
explicit verified recovery. Exclusive migration locking, actual SQL, database
privilege separation, backups/restoration and mixed-version runtime tests remain
Phase 3/7 gates. PostgreSQL is the only selected first-release store adapter.

## Endpoint and transport profile v1

Runtime configuration has optional transportProfile=production|loopback; omitted
means production. Manifest clients have the same optional field. The selected
client and runtime profiles must match. All manifest client callbacks are validated
under their declared profile; runtime issuer/OpenFGA endpoints use the runtime
profile. Structural validation must precede these checks. Browser requests cannot
select a transport profile or override the configured endpoints.

Production requires HTTPS. Explicit loopback mode additionally permits HTTP only
for exact localhost, 127.0.0.1 or [::1], with optional canonical decimal port 1–65535.
It never permits remote HTTP, wildcard listeners, other 127/8 spellings, short/octal/
hex numeric hosts or a localhost suffix. HTTPS remains allowed in either profile.
The runtime transport must enforce loopback resolution for local HTTP, reject
unsafe credential-bearing redirects, and use the selected secure cookie policy;
these are later transport/HTTP acceptance gates, not guarantees of URL validation.

Endpoint configuration uses lowercase ASCII DNS labels (IDNs supplied as punycode),
standard IPv4 or lowercase bracketed IPv6 without zone IDs. It rejects credentials,
queries, fragments, whitespace, backslashes, malformed ports and trailing DNS dots.
Paths permit URI path characters and well-formed percent-encoded UTF-8. Dot segments,
encoded path separators and ASCII control/space characters are rejected. Unescaped
characters outside the URI path grammar must be percent-encoded. Input length is
bounded to 2048 ASCII bytes. These deliberate input rules eliminate parser ambiguity
between Go and Node; they do not assert endpoint reachability or issuer trust.

Validated values are never rewritten. A trailing slash, explicit default port or
percent-encoding spelling remains part of the exact configured issuer identity.
Tokens must match that configured issuer. Production and local development must
use separate client/environment configuration; local cookies must not use __Host
names or silently weaken a production application's cookie settings.

## Enterprise evidence amendment (pre-release v1)

Session payloads may now carry enterpriseEvidence, validated by the dedicated
enterprise-evidence schema. Absence represents no enterprise authentication proof;
a password session must reauthenticate before entering a strict workspace.
Evidence binds product/environment, issuer/subject, client, tenant, organization,
policy version, provider ID/protocol, upstream subject and provider session ID.
It includes authTime and verifiedAt as integer Unix milliseconds. authTime is the
signed downstream auth_time converted from seconds; it must equal verifiedAt
rounded down to a whole second for the pinned intent/session flow.

Evidence is created only after verifying the provider intent, linked subject and
organization, provider session, signed downstream token and its exact sid/auth_time
binding, and the locally claimed browser login transaction. This schema/helper
cannot manufacture or verify that provenance. Never accept evidence from a browser,
role claim, email match, account link or arbitrary metadata. Do not store assertions,
intent tokens or provider credentials in evidence. Persist it inside the encrypted
session record, atomically with local login completion.

The internal validateEnterpriseEvidence / ValidateEnterpriseEvidence gate requires
schema-validated authenticated evidence, current trusted target scope and an explicit
positive maxAgeMs from policy. It rejects every scope mismatch, missing evidence,
future verification, inconsistent signed/intent time and exact age expiry. It does
not replace the session expiry/state, authoritative identity, membership or audit
gates. Refresh must not advance authTime/verifiedAt; reauthentication is required
to obtain new evidence. A single evidence object proves only its selected tenant;
changing target tenant/provider/policy requires a new compliant login. Products may
share an organization without sharing product sessions or membership.

Strict login transactions now require providerId and protocol in tenantPolicy;
non-strict transactions may omit both. The claim validator compares both to the
current trusted policy before dispatch. Provider changes invalidate pending claims.

This amends unreleased v1, not a rolling-compatible production format. Existing
non-enterprise session vectors remain valid, but older additionalProperties:false
readers reject records carrying new evidence. Older strict login payloads without
provider selection are rejected. No persistent runtime has shipped: before runtime
rollout, storage compatibility must advertise the finalized reader/writer contract.
Do not deploy mixed old/new readers against these records or silently strip evidence.

The enterprise-two-tenants-v1 corpus derives sanitized evidence from the live
two-tenant/one-client probe. Fixed evaluation times keep it deterministic. Both
runners exercise tenant/subject/policy/age rejection, including a previous account
with all other expected fields held constant. These fixtures do not replace live
provider verification or prove browser session replacement.

## Pinned model readiness

The internal TS/Go model-binding guard accepts structurally validated manifest
and binding values and recomputes the complete compiler artifact. The supplied
model, hash, role mapping and permission mapping must all match. Construction
snapshots these values; no mutable caller artifact becomes a trusted mapping.

Verification reads exactly the configured OpenFGA store/model pair. It never
uses latest-model discovery or writes a model. A five-second budget includes
credential loading and the read; caller cancellation can shorten it. Redirects
are rejected, responses are limited to one MiB, and credentials/errors never
appear in the result. Credential callbacks must honor cancellation; the SDK
cannot forcibly terminate arbitrary callback work.

The returned model ID and full model must match. Comparison adds only the
pinned API's protobuf defaults: empty conditions, absent user relations and
metadata, empty computed-userset object, metadata module/source_info, and empty
relation-reference condition. Unknown fields remain in comparison and fail.
Local mismatch is invalid_argument; remote mismatch, missing model, malformed
response or credential failure is unavailable; cancellation remains distinct.
Shared model-binding.json fixtures capture the pinned provider response and
drift cases. Verified results bind mappings to that exact immutable model ID;
future checks must send that ID. This guard does not implement runtime check
orchestration, role mutation, model activation, or tuple migration.

## Audited execution

[audited-decision.md](audited-decision.md) defines the internal read-only decision
runner's durable ordering, fresh invocation IDs, cancellation finalization budget,
receipt verification and uncertainty behavior. It requires trusted SDK metadata;
it does not authenticate actors or implement public authorization by itself.

## Runtime configuration

[runtime-configuration.md](runtime-configuration.md) defines bounded deployment
loading, structural and semantic policy checks, immutable/detached snapshots,
and the boundary between locally valid settings and remote readiness.

## OIDC discovery

[oidc-discovery.md](oidc-discovery.md) defines bounded issuer-pinned metadata
reads, same-origin endpoints, required backend capabilities and cancellation.

## Audited authentication

[audited-authentication.md](audited-authentication.md) defines durable authentication
completion, verified/unknown actor construction, receipt checks, cancellation and
post-commit expiry/freshness refusal in both SDKs.

## Core runtime entry point

[core-runtime.md](core-runtime.md) specifies the implemented development constructors,
scoped durable ports, five-second startup gate and audited bearer authentication.
Sessions and authorization are not yet exposed.

## Managed operation persistence

[managed-operations.md](managed-operations.md) defines actor-scoped primary journal
reads and atomic idempotent local creation, including audit and uncertain COMMIT
semantics. Public managed orchestration and remote transitions remain pending.

## Managed candidate source

[managed-candidates.md](managed-candidates.md) defines bounded primary keyset reads,
canonical bigint positions and principal/scope binding. These internal hints still
require authenticated, audited membership checks in the public discovery layer.
