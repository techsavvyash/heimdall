# Working in Heimdall

## Scope and design

- Read `design/content/decisions.md` and the relevant contract before implementing behavior.
- TypeScript and Go are both initial-release SDKs. Share behavior through `contracts/fixtures/`.
- `design/content/explorations/` contains unapproved alternatives. Implement those only when selected by the user.
- Keep auth code inside SDK packages. Product examples own business data and integration code.
- Add module directories when their implementation starts. Current empty directories mark agreed workspace boundaries.

## Setup

Use Node 24 and Go 1.24 or newer. Run `nvm use`, `npm ci`, and `npm run docs:install` from the repository root. npm installs the tracked Git hooks through `prepare`. Existing custom hook paths require explicit integration.

Quartz has an independent dependency tree under `design/`. Its vendored engine is excluded from SDK linting and formatting. Edit `design/content/` for documentation.

## Feedback loop

- `npm run check:fast` checks formatting, Oxlint, TypeScript, Knip, Go formatting and Go vet.
- `npm test` runs Vitest once. `npm run test:watch` runs the watch loop.
- `npm run test:go` runs Go tests with the race detector.
- `npm run build` bundles with Rolldown and emits TypeScript declarations with tsc.
- `npm run check` runs all workspace checks and builds Quartz. This is the pre-push and CI gate.
- Use `npm run format` and `gofmt -w sdk/go` to apply formatting. Hooks only check files.

## Implementation rules

- Preserve product and tenant boundaries, identity verification, error distinctions and cancellation.
- Add shared fixtures and focused regression tests when implementing nontrivial behavior.
- Avoid placeholder public APIs. The initial SDK entry points intentionally contain no runtime implementation.
- Vitest currently permits an empty test suite during scaffolding. Remove `passWithNoTests` when the first tests land.
- Keep package scripts and Knip entries accurate. Do not suppress unused findings broadly.
- Keep secret values, local environment files, generated output and dependencies out of commits.
- Stage intended paths. Legacy root documentation and local artifacts still exist from the previous design.
- Do not skip hooks to make a commit pass. Fix the failure and rerun the narrow check first.
