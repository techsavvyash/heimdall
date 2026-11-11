---
title: Development workspace and feedback loop
date: 2026-11-07
---

The workspace is scaffolded for TypeScript and Go. Runtime functionality is tracked in [[task-list]]. The built-in persistence adapter remains an exploration in [[explorations/built-in-adapters]].

## Setup

Use Node 24, pnpm 12.10.1 and Go 1.24 or newer. From the repository root:

```sh
nvm use
pnpm install --frozen-lockfile
pnpm run check
```

pnpm workspaces manage the TypeScript package and Quartz. Quartz shares the root workspace lockfile and install. The Go module is `github.com/techsavvyash/heimdall/sdk/go`. The development TypeScript package is private and named `@heimdall/sdk` pending publication decisions.

## Fast feedback

| Tool | Responsibility | Command |
| --- | --- | --- |
| Oxfmt | Formatting owned SDK and workspace files | `pnpm run format:check` |
| Oxlint | Correctness rules and explicit TypeScript rules | `pnpm run lint` |
| TypeScript | Strict types and declaration generation | `pnpm run typecheck` |
| Knip | Unused files, exports and dependencies in SDK workspace | `pnpm run knip` |
| Vitest | TypeScript tests and watch loop | `pnpm test`, `pnpm run test:watch` |
| Rolldown | ESM SDK bundle and source map | `pnpm run build` |
| Go tooling | Formatting, vet and race-enabled tests | `pnpm run check:go`, `pnpm run test:go` |
| Quartz | Design-site build | `pnpm run docs:build` |

`pnpm run check:fast` combines formatting, lint, types, Knip and Go formatting/vet. `pnpm run check` adds test runners, SDK build and the Quartz build. Git hooks run the fast command before commits and the full command before pushes. CI runs the same full command after clean installs.

Hooks check files without rewriting them. Use `pnpm run format` and `gofmt -w sdk/go` to apply formatting. Node 24 and Go must be available on the invoking shell's PATH. pnpm's prepare step installs the tracked `.githooks` path and refuses to overwrite an unrelated custom hook path.

## Scaffold boundaries

The current SDKs contain empty entry points. Vitest temporarily permits an empty test suite, and Go reports no test files. Remove the Vitest allowance when the first contract tests land. Future meaningful tests belong alongside implementation and in shared fixtures.

Rolldown bundles JavaScript. TypeScript emits declarations separately. The initial package output is ESM, while supported runtime and publication targets remain implementation decisions. The Go module has no external dependencies yet.

The formatter and Knip target owned workspace code. Quartz's vendored engine has separate scope. `AGENTS.md` records the commands and design boundaries for future agent tasks.

## Dependency release age

The root `pnpm-workspace.yaml` sets `minimumReleaseAge: 43200` with strict enforcement. This represents 30 days. It applies to the SDK and Quartz, including transitive dependencies. CI uses the shared frozen `pnpm-lock.yaml`. The pinned pnpm version is recorded in the root package manifest.

As of November 8, 2026, the 30-day policy selects Knip's Zod 4.6.5 and Quartz's minimatch 10.2.6. The audit reports a high-severity Zod advisory and a moderate minimatch advisory. Patched releases are newer than the allowed age, so dependency updates or an explicitly approved policy exception are required to clear them. [Zod advisory](https://github.com/advisories/GHSA-mrhx-c7xc-m3w5) · [minimatch advisory](https://github.com/advisories/GHSA-g3hv-75wg-m249)

### Diagram validation

Every docs build runs `pnpm --filter @jackyzha0/quartz run check:mermaid`
before Quartz. It parses every Mermaid block using Mermaid 11.4.0, matching
the Quartz browser renderer. Errors report the Markdown path and opening line
and fail the build. In sequence-diagram message labels, use commas instead of
literal semicolons: Mermaid interprets semicolons as statement separators.
Parser validation complements checking rendered diagrams in the browser.
