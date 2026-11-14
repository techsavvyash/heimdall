---
title: Phase 6 tenant SAML and OIDC setup
date: 2026-11-14
---

Both SDKs can prepare and read back a ZITADEL identity provider scoped to a verified organization. The product owns the settings route, administrator authorization, tenant-to-organization mapping and publication of a versioned login policy. The SDK owns provider request defaults, readback, uncertainty handling and verification of the test login.

```mermaid
sequenceDiagram
  participant A as Product administrator
  participant P as Product settings workflow
  participant S as Heimdall SDK
  participant Z as ZITADEL
  participant I as Customer identity provider
  A->>P: Submit provider settings for a workspace
  P->>P: Authorize administrator and resolve organization
  P->>S: Prepare candidate with durable dispatch claim
  S->>P: Claim creation attempt
  S->>Z: Create provider in resolved organization
  S->>Z: Read back owner, state and settings
  S-->>P: Return verified candidate and registration URL
  A->>I: Register ZITADEL as relying party
  A->>P: Start restricted candidate login
  P->>S: Start federation under verification workspace policy
  S->>Z: Bind login to exact provider and policy version
  Z->>I: Authenticate test account
  I-->>Z: Return federation result
  Z-->>S: Complete bound authentication
  S-->>P: Return verified browser session
  P->>S: Check current principal against exact candidate
  P->>P: Compare and swap production policy and record change
```

## Product wiring

1. Authorize the initiating administrator and resolve the target organization from trusted workspace storage. Store the candidate version and creation intent durably. Pass a privileged credential from secret storage. The dispatch callback must claim an unused attempt atomically and return an error for a prior claim. Keep the settings credential out of login handlers and client code.
2. Call TypeScript `prepareEnterpriseProvider` from `@techsavvyash/heimdall-sdk/provisioning`, or Go `provisioning.PrepareEnterpriseProvider`. Supply the HTTPS OIDC issuer and client registration, or the SAML IdP metadata XML bytes. The SDK requests PKCE for OIDC and signed Redirect requests for SAML. It disables automatic creation, linking and updates. The customer registers the returned OIDC callback URL or imports the SAML service-provider metadata URL. The provider is active in ZITADEL after creation. Product policy publication is a separate step.
3. Use an administrator-restricted verification workspace ID and a unique candidate policy version. Give this workspace no application memberships or resources. Make its policy available to the existing federation start and callback handlers while the candidate is pending. Use a test account in the intended organization with a verified provider link. Reserve the verification workspace ID for this workflow and use a separate browser context for the test account so an operator session can still authorize publication.
4. Authenticate the test session through the SDK. Call TypeScript `heimdall.verifyEnterpriseProviderTest` or Go `client.VerifyEnterpriseProviderTest` with the resulting principal, verification workspace ID and server-stored candidate policy. Both SDKs require fresh evidence from that runtime and cap test login age at five minutes. Recheck administrator authority and compare and swap the expected production policy version and candidate version before publishing. Record the change through the product's durable audit integration. Retire the verification mapping and log out the test session. Evidence from that workspace cannot authorize a production workspace.

After an ambiguous create, recover the exact provider ID from ZITADEL and pass it explicitly for read-only reconciliation. The SDK verifies organization, provider ID, name, active state, protocol and returned public settings. If the ID is unknown, stop and reconcile the original creation attempt before authorizing another. ZITADEL readback omits the OIDC client secret and PKCE setting. The live login exercises the registration in use. For SAML, the SDK verifies the uploaded XML bytes and returned signature, binding and NameID settings.

## Acceptance evidence

Each SDK created and recovered a provider for each protocol against the pinned Railway ZITADEL deployment with a disposable Keycloak directory. All four runs rejected readback under another organization.

| Provider creation | OIDC | SAML |
| --- | --- | --- |
| TypeScript | `66c3c9a2-3724-4191-9f4e-2d193a697029` | `09e4ce9a-85d7-4b7a-8f9a-2fdcef5779a3` |
| Go | `816943f8-59a2-4a53-b9ee-c11b64d5a85e` | `32afeaea-f3b3-4e6f-9fdc-c9d2e0251fe0` |

OIDC run `65b7753d-7039-4a93-b74e-a83d261e0f4a` and SAML run `6f5ea804-3267-4eef-a1a7-50d12018dcb3` exercised both SDKs' browser callbacks and candidate verification. Both accepted the matching login and rejected a different verification workspace, rotated candidate version, wrong intent, missing browser cookie and replay. The harness used temporary session storage for these SDK checks. [[phase-6-tenant-sso-gate]] records deployed browser login, encrypted PostgreSQL session persistence and account switching in the reference products.

Run `tests/integration/federation.mjs` with the protected variables described in [[phase-2-browser-federation]], plus `HEIMDALL_PROBE_ENTERPRISE_SETUP=typescript` or `go` and `HEIMDALL_PROBE_SDK_FEDERATION_HTTP=1`. The probe creates and removes its own organizations, directory realm and sessions. The production policy compare and swap is product-owned and the standalone harness exercises the SDK check preceding it.

Source commit `da666fa9e4d11f1e17f41190f43d372335865518` passed the workspace gate with 1,778 Vitest tests, Go race tests, package checks and 248 Mermaid diagrams checked in both Markdown and generated HTML. GitHub Actions runs `43514856120` and `43514865017` passed. Railway docs deployment `19ab3be2-f23d-4449-b8b0-b2c1109b18d7` served this evidence for that commit, and Chromium rendered the sequence diagram.

## Recover a lost provider creation response

The verification relay forwards one creation call to Railway ZITADEL. After ZITADEL confirms creation, the relay captures the provider ID for the test operator and drops the SDK connection. The SDK reports `outcome_unknown`. Reusing the claimed attempt returns `conflict` without forwarding a second creation. The test operator then supplies the captured ID and the SDK verifies it directly against Railway. The probe also submits an empty OIDC client secret or empty SAML metadata before claiming the attempt. Both fail locally.

```mermaid
sequenceDiagram
  participant S as Heimdall SDK
  participant R as Verification relay
  participant Z as Railway ZITADEL
  S->>S: Claim the creation attempt
  S->>R: Send provider creation
  R->>Z: Forward one request
  Z-->>R: Confirm creation with provider ID
  R--xS: Drop the response
  S->>S: Reject another dispatch on the same claim
  S->>Z: Read back the operator supplied provider ID
  Z-->>S: Return provider scope and public settings
```

Set `HEIMDALL_PROBE_SETUP_RESPONSE_LOSS=1` with the existing enterprise setup probe. The fixture claim is held in memory for one test run. Production setup must persist the claim independently so it survives restart. The relay gives the test the exact resource ID and does not exercise an operator finding that ID after an incident.

All four runs recovered the committed provider, rejected readback under another organization and completed federation with the recovered provider.

| SDK | OIDC run | SAML run |
| --- | --- | --- |
| TypeScript | `2834fed5-8cb1-4245-94af-310dd865c0e6` | `acd94172-bcd0-4112-8249-9932ebef4aa3` |
| Go | `ae825d9f-3dc4-4587-992f-ad5eac7bd7f2` | `abbe8697-4459-44d5-bf6d-683b3ff9b8b5` |

## Reference product settings routes

Ledger and Support mount the same three POST routes under `/workspaces/{workspace}/enterprise/provider/`. Apply `examples/enterprise-provider.sql` to the product database with `psql -v product_schema=ledger` or `-v product_schema=support`. Grant the runtime select, insert and update access to the two product tables. Apply the migration even when the settings routes are disabled so both backends can read published policies. Backup and restore them with the product database. Keep their `verification` and production data in separate deployments and scopes.

Set `HEIMDALL_PROVIDER_SETTINGS` to a trusted map from workspace ID to `{organizationId, testSubject}`. Select an organization whose identity administration the product has verified. Supply a privileged ZITADEL credential through `HEIMDALL_PROVIDER_SETTINGS_TOKEN`, separate from `HEIMDALL_FEDERATION_CREDENTIAL`. The product gets the organization from the trusted map. The request accepts a version consisting of 32 lowercase hexadecimal characters. Generate a fresh version for each intended change and keep that version during recovery.

| Call | Actor | Request and effect |
| --- | --- | --- |
| `prepare` | Workspace owner with a session that satisfies the active policy | Send `version`, `protocol` and OIDC `issuer`, `clientId`, `clientSecret` or base64 SAML `metadataXml`. Returns the provider registration details and a random verification workspace ID. |
| `test` | The configured test subject | In a separate browser profile, use `/auth/federation?workspace={verificationWorkspaceId}`. Submit `version` with the resulting session. The provider test must match the candidate's version, protocol, organization and provider. |
| `activate` | Workspace owner with a session that satisfies the active policy | Submit `version` and `expectedPolicyVersion`, using JSON null if the workspace has no prior strict policy. The product checks a fresh test and inserts the policy and corresponding audit outbox record atomically. |

The reference uses candidate login evidence up to four minutes old and allows 45 seconds between recording that test and publication. The verification workspace is reserved for login and has no application resources or memberships. Use a dedicated test subject with no application memberships or product operator authority because successful test login authenticates that identity to the application. Log out the test session after verification. Publication retires the verification route and the earlier production evidence. The owner starts a new login under the newly published policy.

The creation claim is persisted before dispatch. Clear-text OIDC secrets are sent only to ZITADEL in the creation request. For an uncertain response, find the exact provider ID using the candidate's generated name `heimdall-{product}-{version}` under the mapped organization. Submit `prepare` with the same public settings and `existingProviderId`. Omit the OIDC client secret for verification. If the provider is unknown, reconcile the first attempt with the operator before issuing another candidate version.

The directory job uses the same PostgreSQL policy source and needs the product database URL for its authorization checks. Give it read access to the publication and candidate tables. The reference SIEM job stages `product.sso.policy_published` using the publication row's stable event ID, original timestamp, tenant, actor and old and new policy versions. Set the job's product database URL and `HEIMDALL_PROVIDER_SETTINGS` alongside its usual tenant-specific SIEM settings. The job can replay a failed write using the same event ID. The product's publication row retains its audit data even when the central SDK store or the receiver is temporarily unavailable. Give one scheduled job ownership of the `siem` destination for that tenant.

```mermaid
flowchart LR
  A[Owner submits candidate] --> R[Product verifies owner and resolves organization]
  R --> C[PostgreSQL claims one dispatch]
  C --> Z[SDK creates and reads ZITADEL provider]
  Z --> V[Test subject signs in to verification workspace]
  V --> T[SDK verifies exact candidate evidence]
  T --> P[Owner publishes expected policy version]
  P --> D[PostgreSQL records policy and audit event together]
  D --> L[Next login and role check use new policy]
  D --> J[Product audit job copies event to retained audit]
  J --> S[Tenant SIEM destination]
```


### Deployed settings verification

Run `e88b93d6-56c2-4cb0-940f-70b4db269be3` passed against the pinned Railway stack with SAML in Ledger deployment `034e2923-f462-49d4-b7f0-d7f8361da0de` and OIDC in Support deployment `aa4bf0fe-3b28-49e0-9928-db646b153280`. The probe used an organization-scoped `ORG_SETTINGS_MANAGER` credential and a separate federation credential. An owner from the other workspace, an unmapped route and a caller-supplied organization failed before a provider appeared. A database trigger then rejected storage of the created provider ID. Both apps retained the dispatch claim, rejected a duplicate create and recovered the sole new provider by exact ID. Readback using the prior provider ID failed.

Activation before the test, a test using the existing production session, publication by the other workspace owner and an incorrect expected version were rejected. A candidate login with the configured test account had no workspace access. After the exact test and activation, the old cookie lost access, an in-flight callback under the old policy failed and the verification workspace stopped accepting sign-ins. The new production login restored only the intended workspace's access. The probe matched each activation response to a single durable, tenant-scoped policy publication and audit outbox row.

| Product | Publication audit event ID |
| --- | --- |
| Ledger | `237a233c-f57b-47f3-8c0b-1940c459749a` |
| Support | `125b911977d2a1fbff22849574ca07da` |

Reproduce with `HEIMDALL_PROBE_PROVIDER_SETTINGS=1` and the Railway configuration used by `tests/integration/federation-switch.mjs`. The probe uses disposable organizations, provider registrations, OpenFGA stores and product policy rows. It verifies the product outbox before cleanup. [[phase-6-siem-job#live-publication-and-receipt]] verifies export from a subsequent run through both scheduled job commands. [Pinned ZITADEL provider listing API](https://zitadel.com/docs/reference/api/management/zitadel.management.v1.ManagementService.ListProviders)
