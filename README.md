# Heimdall

A shared authentication and authorization design built around ZITADEL, OpenFGA, and TypeScript and Go SDKs. The repository currently contains the design and SDK development scaffolding.

## Start working

Install Node 24 and Go 1.24 or newer, then run:

```sh
nvm use
npm ci
npm run docs:install
npm run check
```

`npm ci` installs the local pre-commit and pre-push hooks. Hook checks require Node 24 and Go on your shell's PATH. Use `nvm use` before committing or pushing.

## Commands

| Command              | Purpose                                                     |
| -------------------- | ----------------------------------------------------------- |
| `npm run check:fast` | Formatting, lint, types, unused code, Go formatting and vet |
| `npm test`           | TypeScript tests with Vitest                                |
| `npm run test:watch` | Vitest watch mode                                           |
| `npm run test:go`    | Go tests with race detection                                |
| `npm run build`      | Rolldown bundle and TypeScript declarations                 |
| `npm run check`      | Full local, pre-push and CI gate, including Quartz build    |
| `npm run format`     | Format owned workspace files with Oxfmt                     |
| `npm run docs:dev`   | Preview Quartz at localhost:8080                            |

The SDK scaffold has no runtime behavior or test cases yet. Vitest allows an empty suite until the first contract tests are implemented. The TypeScript package is private during development and currently targets ESM. Runtime compatibility and publication scope will be decided with the SDK implementation.

## Layout

```text
contracts/fixtures/       Shared language-independent behavior fixtures
policies/tenant-rbac/     Authorization model and assertions
sdk/typescript/          Rolldown, Oxlint, Oxfmt, Vitest and strict TypeScript
sdk/go/                  Go module, gofmt, go vet and race-enabled tests
examples/ledger/         Future TypeScript product integration
examples/support/       Future Go product integration
tests/integration/       Cross-service scenarios
deploy/local/            Future local auth deployment
scripts/                 Workspace checks and hook installation
.githooks/               Fast pre-commit and full pre-push checks
design/                  Quartz site with an independent lockfile
```

See [the design index](design/content/index.md), [shared SDK contract](design/content/shared-sdk-contract.md), and [task list](design/content/task-list.md). Built-in persistence adapters remain an exploration. Read [AGENTS.md](AGENTS.md) for the agent feedback loop.
