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
