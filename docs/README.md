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

The production site is https://docs-production-7570859e.up.railway.app/ and analytics are disabled. `configuration.baseUrl` points to the Railway domain. Only `content/` is published. Dependencies, build output, and Quartz caches are ignored by Git.

## Design status

The first-release scope is locked, including SSO, SCIM user lifecycle and SIEM. SDK API spellings and executable models remain implementation work. See `content/design/decisions.md` for agreed constraints and open decisions. The auth system has not been implemented or exercised against running ZITADEL and OpenFGA services.

## Upstream

Bootstrapped from Quartz 5 at commit `97a2d05f80c4c50534959b1d0d41cc4b3895625e`. Quartz source is vendored here with its MIT license in `LICENSE.txt`.

## Verification

Validated on November 7, 2026 using Node 24.13.1. The production build succeeds, all 19 Markdown pages have resolving internal links, and all 45 Mermaid diagrams rendered as SVG in the browser.

Workspace dependencies are pinned in the root `pnpm-lock.yaml`. The release-age policy can require older versions than upstream latest.

For the documentation container, use the repository root as its build context with `docker build -f docs/Dockerfile .`.

## Publishing

Railway hosts the static Quartz output in the dedicated Heimdall project, production environment, docs service. The service builds from the repository root with `RAILWAY_DOCKERFILE_PATH=docs/Dockerfile`. The multi-stage image builds with Node 24 and pnpm 12.10.1, then serves only `docs/public/` using Caddy on Railway's `PORT` (8080 locally). Clean URLs and real HTTP 404 responses are supported.

The service tracks `feat/docs-website` during the design phase. Switch its source branch to `main` after integration. GitHub Actions continues to check the workspace; GitHub Pages publishing is removed.

For a manual deployment, link the Railway CLI to the Heimdall project and run `railway up --service docs` from the repository root after `pnpm run check`. When changing the public domain, update `configuration.baseUrl` and the documentation links before rebuilding.
