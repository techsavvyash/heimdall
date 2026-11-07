---
title: Development workspace and feedback loop
date: 2026-11-07
---

The workspace is scaffolded for TypeScript and Go. Runtime functionality is tracked in [[task-list]]. The built-in persistence adapter remains an exploration in [[explorations/built-in-adapters]].

## Setup

Use Node 24 and Go 1.24 or newer. From the repository root:

```sh
nvm use
npm ci
npm run docs:install
npm run check
```

npm workspaces manage the TypeScript package. Quartz retains its own lockfile and install step under `design/`. The Go module is `github.com/techsavvyash/heimdall/sdk/go`. The development TypeScript package is private and named `@heimdall/sdk` pending publication decisions.

## Fast feedback

| Tool | Responsibility | Command |
| --- | --- | --- |
| Oxfmt | Formatting owned SDK and workspace files | `npm run format:check` |
| Oxlint | Correctness rules and explicit TypeScript rules | `npm run lint` |
| TypeScript | Strict types and declaration generation | `npm run typecheck` |
| Knip | Unused files, exports and dependencies in SDK workspace | `npm run knip` |
| Vitest | TypeScript tests and watch loop | `npm test`, `npm run test:watch` |
| Rolldown | ESM SDK bundle and source map | `npm run build` |
| Go tooling | Formatting, vet and race-enabled tests | `npm run check:go`, `npm run test:go` |
| Quartz | Design-site build | `npm run docs:build` |

`npm run check:fast` combines formatting, lint, types, Knip and Go formatting/vet. `npm run check` adds test runners, SDK build and the Quartz build. Git hooks run the fast command before commits and the full command before pushes. CI runs the same full command after clean installs.

Hooks check files without rewriting them. Use `npm run format` and `gofmt -w sdk/go` to apply formatting. Node 24 and Go must be available on the invoking shell's PATH. npm's prepare step installs the tracked `.githooks` path and refuses to overwrite an unrelated custom hook path.

## Scaffold boundaries

The current SDKs contain empty entry points. Vitest temporarily permits an empty test suite, and Go reports no test files. Remove the Vitest allowance when the first contract tests land. Future meaningful tests belong alongside implementation and in shared fixtures.

Rolldown bundles JavaScript. TypeScript emits declarations separately. The initial package output is ESM, while supported runtime and publication targets remain implementation decisions. The Go module has no external dependencies yet.

The formatter and Knip target owned workspace code. Quartz's vendored engine and legacy root documents have separate scope. `AGENTS.md` records the commands and design boundaries for future agent tasks.
