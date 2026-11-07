# Working in Heimdall

## Scope and design

- Read `docs/content/design/decisions.md` and the relevant contract before implementing behavior.
- TypeScript and Go are both initial-release SDKs. Share behavior through `contracts/fixtures/`.
- `docs/content/design/explorations/` contains unapproved alternatives. Implement those only when selected by the user.
- Keep auth code inside SDK packages. Product examples own business data and integration code.
- Add module directories when their implementation starts. Current empty directories mark agreed workspace boundaries.

## Setup

Use Node 24, pnpm 12.10.1 and Go 1.24 or newer. Run `nvm use` and `pnpm install --frozen-lockfile` from the repository root. pnpm installs the tracked Git hooks through `prepare`. Existing custom hook paths require explicit integration.

Quartz and the TypeScript SDK share the root pnpm workspace and lockfile. The vendored Quartz engine is excluded from SDK linting and formatting. Edit `docs/content/` for documentation and `docs/content/design/` for design proposals.

## Feedback loop

- `pnpm run check:fast` checks formatting, Oxlint, TypeScript, Knip, Go formatting and Go vet.
- `pnpm test` runs Vitest once. `pnpm run test:watch` runs the watch loop.
- `pnpm run test:go` runs Go tests with the race detector.
- `pnpm run build` bundles with Rolldown and emits TypeScript declarations with tsc.
- `pnpm run check` runs all workspace checks and builds Quartz. This is the pre-push and CI gate.
- Use `pnpm run format` and `gofmt -w sdk/go` to apply formatting. Hooks only check files.

## Implementation rules

- Preserve product and tenant boundaries, identity verification, error distinctions and cancellation.
- Add shared fixtures and focused regression tests when implementing nontrivial behavior.
- Avoid placeholder public APIs. The initial SDK entry points intentionally contain no runtime implementation.
- Vitest currently permits an empty test suite during scaffolding. Remove `passWithNoTests` when the first tests land.
- Keep package scripts and Knip entries accurate. Do not suppress unused findings broadly.
- Keep secret values, local environment files, generated output and dependencies out of commits.
- Stage intended paths. Keep screenshots and local debugging artifacts out of the repository root.
- Do not skip hooks to make a commit pass. Fix the failure and rerun the narrow check first.

The workspace enforces a 30-day minimum dependency release age. Resolve compatible mature versions instead of adding release-age exceptions. Keep `pnpm-lock.yaml` synchronized and validate with `pnpm install --frozen-lockfile`.
