# Heimdall documentation site

Quartz 5 documentation for the SDK-first Heimdall design. Source content lives in `content/`, with architecture and SDK design under `content/design/`.

## Local development

Use Node 24 and the pnpm version pinned in the root package manifest. Run installation from the repository root.

```sh
nvm use
pnpm install --frozen-lockfile
pnpm run docs:dev
```

Open http://localhost:8080. Run `pnpm run docs:build` from the root to generate `docs/public/`.

The production site is https://techsavvyash.github.io/heimdall/ and analytics are disabled. `configuration.baseUrl` includes the GitHub Pages project path. Only `content/` is published. Dependencies, build output, and Quartz caches are ignored by Git.

## Design status

The first-release scope is locked, including SSO, SCIM user lifecycle and SIEM. SDK API spellings and executable models remain implementation work. See `content/design/decisions.md` for agreed constraints and open decisions. The auth system has not been implemented or exercised against running ZITADEL and OpenFGA services.

## Upstream

Bootstrapped from Quartz 5 at commit `97a2d05f80c4c50534959b1d0d41cc4b3895625e`. Quartz source is vendored here with its MIT license in `LICENSE.txt`.

## Verification

Validated on November 7, 2026 using Node 24.13.1. The production build succeeds, all 19 Markdown pages have resolving internal links, and all 45 Mermaid diagrams rendered as SVG in the browser.

Workspace dependencies are pinned in the root `pnpm-lock.yaml`. The release-age policy can require older versions than upstream latest.

For the documentation container, use the repository root as its build context with `docker build -f docs/Dockerfile .`.

## Publishing

The `Publish documentation` workflow builds and checks the workspace, uploads only `docs/public/`, and deploys through GitHub Pages. Pushes to `feat/docs-website` publish during the initial design phase; pushes to `main` publish after integration. Remove the feature-branch trigger and environment rule when that branch is retired. Pull requests do not deploy. The `github-pages` environment restricts deployment to those two branches. GitHub Pages must use the Actions build type. No Docker image build is involved.
