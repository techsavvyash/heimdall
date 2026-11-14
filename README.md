# Heimdall

A shared authentication and authorization design built around ZITADEL, OpenFGA, and TypeScript and Go SDKs. The repository contains the agreed design and SDK implementation in progress.

## Start working

Install Node 24, pnpm 12.10.1 and Go 1.24 or newer, then run:

```sh
nvm use
pnpm install --frozen-lockfile
pnpm run check
```

`pnpm install --frozen-lockfile` installs the local pre-commit and pre-push hooks. Hook checks require Node 24 and Go on your shell's PATH. Use `nvm use` before committing or pushing.

## Commands

| Command               | Purpose                                                     |
| --------------------- | ----------------------------------------------------------- |
| `pnpm run check:fast` | Formatting, lint, types, unused code, Go formatting and vet |
| `pnpm test`           | TypeScript tests with Vitest                                |
| `pnpm run test:watch` | Vitest watch mode                                           |
| `pnpm run test:go`    | Go tests with race detection                                |
| `pnpm run build`      | Rolldown bundle and TypeScript declarations                 |
| `pnpm run check`      | Full local, pre-push and CI gate, including Quartz build    |
| `pnpm run format`     | Format owned workspace files with Oxfmt                     |
| `pnpm run docs:dev`   | Preview Quartz at localhost:8080                            |

Both SDKs expose audited bearer-token authentication and tenant authorization, PostgreSQL browser sessions, workspace discovery and managed onboarding. Ledger and Support demonstrate those integrations in TypeScript and Go. Follow the [reference product setup](docs/content/evidence/phase-4-reference-backends.md) and [implementation phases](docs/content/implementation-phases.md) for the verified scope and release progress. The ESM-only TypeScript package is distributed as a verified tarball on GitHub Releases.

## Layout

```text
contracts/fixtures/       Shared language-independent behavior fixtures
policies/tenant-rbac/     Authorization model and assertions
sdk/typescript/          Rolldown, Oxlint, Oxfmt, Vitest and strict TypeScript
sdk/go/                  Go module, gofmt, go vet and race-enabled tests
examples/ledger/          TypeScript Ledger product integration
examples/support/         Go Support product integration
tests/integration/       Cross-service scenarios
deploy/local/            Future local auth deployment
scripts/                 Workspace checks and hook installation
.githooks/               Fast pre-commit and full pre-push checks
docs/                    Quartz site in the pnpm workspace
```

See [the documentation index](docs/content/index.md), [shared SDK contract](docs/content/design/shared-sdk-contract.md), and [task list](docs/content/design/task-list.md). See the [adapter design](docs/content/design/built-in-adapters.md) for PostgreSQL integration boundaries. Read [AGENTS.md](AGENTS.md) for the agent feedback loop.

## Dependency policy

`pnpm-workspace.yaml` includes the TypeScript SDK and Quartz. `minimumReleaseAge: 43200` requires releases to be at least 30 days old, including transitive dependencies. Strict mode rejects versions that cannot satisfy the age limit. Both projects share one frozen lockfile.

Documentation: https://docs-production-7570859e.up.railway.app/. First-release scope includes both SDKs, PostgreSQL adapters, tenant SSO, SCIM user lifecycle and durable SIEM integration.
