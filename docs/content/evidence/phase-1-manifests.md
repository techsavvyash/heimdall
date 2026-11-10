---
title: Phase 1 evidence — product manifests
date: 2026-11-11
---

**Phase 1 remains open.** This checkpoint defines the product manifest and implements its internal semantic validators in both languages. It does not implement OpenFGA model generation, role checks or live administration.

## Implemented and checked

- Versioned schema for clients, permission names, roles, owner role and delegation.
- Config clientKey identifies the provisioned manifest client.
- Undeclared permissions, missing owners, unknown delegators/targets and ordinary owner-role grants are rejected.
- Product, manifest version and selected client must match the runtime binding.
- Product-defined owner names are supported; business permissions remain explicit.
- Shared fixtures cover prototype-like role names without treating inherited JavaScript properties as declared roles.

The full workspace gate passed with 114 Vitest tests, Go race tests, lint/type/static checks, SDK build/declarations and Quartz build. Both semantic runners first assert structural validity, so negative cases actually exercise cross-reference checks rather than merely failing shape validation.

## Remaining work

Session/cursor/operation/audit formats, persistence compatibility, complete configuration endpoint parsing and the rest of Phase 1 remain open. Mutation authorization and service model behavior require later live runtime evidence.
