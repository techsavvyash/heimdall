# Working in Heimdall

## Scope and design

- Read `docs/content/design/decisions.md` and the relevant contract before implementing behavior.
- TypeScript and Go are both initial-release SDKs. Share behavior through `contracts/fixtures/`.
- `release-contract.md`, `audit-contract.md` and `enterprise-contract.md` in the design directory lock first-release scope. SSO, SCIM user lifecycle, SIEM and durable audit of every authorization decision/role change/ownership transfer are mandatory.
- `docs/content/design/built-in-adapters.md` is the selected adapter scope. Its historical exploration is superseded; other explorations remain unapproved. Preserve the managed lifecycle gate and uncertain-write recovery limits.
- Keep auth code inside SDK packages. Product examples own business data and integration code.
- Add module directories when their implementation starts. Current empty directories mark agreed workspace boundaries.

## Sequential delivery

- Follow docs/content/implementation-phases.md. Complete the current phase with test evidence, committed changes and a verified Railway deployment before advancing.
- Record partial progress honestly; docs deployment is not runtime validation or phase completion.

## Setup

Use Node 24, pnpm 12.10.1 and Go 1.24 or newer. Run `nvm use` and `pnpm install --frozen-lockfile` from the repository root. pnpm installs the tracked Git hooks through `prepare`. Existing custom hook paths require explicit integration.

Quartz and the TypeScript SDK share the root pnpm workspace and lockfile. The vendored Quartz engine is excluded from SDK linting and formatting. Edit `docs/content/` for documentation and `docs/content/design/` for design proposals.

## Feedback loop

- `pnpm run check:fast` checks formatting, Oxlint, TypeScript, Knip, Go formatting and Go vet.
- `pnpm test` runs Vitest once. `pnpm run test:watch` runs the watch loop.
- `pnpm run test:go` runs Go tests with the race detector and without result caching so changes to shared fixtures outside the Go module are always checked.
- `pnpm run build` bundles with Rolldown and emits TypeScript declarations with tsc.
- `pnpm run check:package` verifies the built TypeScript tarball in an isolated consumer, including its declaration and principal type boundary.
- `pnpm run check` runs all workspace checks and builds Quartz. This is the pre-push and CI gate.
- Use `pnpm run format` and `gofmt -w sdk/go` to apply formatting. Hooks only check files.

## Implementation rules

- Preserve product and tenant boundaries, identity verification, error distinctions and cancellation.
- Add shared fixtures and focused regression tests when implementing nontrivial behavior.
- Avoid placeholder public APIs. Export only implemented, tested behavior; the current public runtime covers audited bearer authentication, not sessions or authorization.
- Vitest must fail on an empty test suite. Both languages consume shared contract fixtures.
- Keep package scripts and Knip entries accurate. Do not suppress unused findings broadly.
- Keep secret values, local environment files, generated output and dependencies out of commits.
- Stage intended paths. Keep screenshots and local debugging artifacts out of the repository root.
- Do not skip hooks to make a commit pass. Fix the failure and rerun the narrow check first.

The workspace enforces a 30-day minimum dependency release age. Resolve compatible mature versions instead of adding release-age exceptions. Keep `pnpm-lock.yaml` synchronized and validate with `pnpm install --frozen-lockfile`.
