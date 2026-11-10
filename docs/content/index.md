---
title: Heimdall documentation
date: 2026-11-08
---

Heimdall brings shared authentication and authorization to product backends through TypeScript and Go SDKs backed by ZITADEL and OpenFGA.

## Start here

The first-release decisions are locked. Read [[design/release-contract|the release contract]], [[design/audit-contract|durable audit]] and [[design/enterprise-contract|enterprise requirements]] before implementing behavior. Documentation describes the target design; the SDK runtime is not implemented.

- [[development-workspace|Development setup and feedback loop]] covers installation, workspace commands, Git hooks and CI.
- [[implementation-roadmap|Implementation roadmap and tickets]] links the PRDs, implementation plans and delivery dependencies.
- [[design/index|System design]] covers architecture, tenancy, SDK contracts, data flows and implementation planning.
- [[design/built-in-adapters|Built-in adapter design]] defines optional PostgreSQL sessions, managed workspaces, recovery and safety boundaries.
- [[design/explorations/built-in-adapters|Earlier adapter exploration]] preserves the alternatives considered.

The repository currently contains SDK scaffolding and the selected first-release design. Historical explorations are marked; executable service behavior remains to be verified.
