# Heimdall design site

Quartz 5 documentation for the SDK-first Heimdall design. Source content lives in `content/`.

## Local development

Use Node 24 (or a supported Node version >=22) and npm >=10.9.2.

```sh
cd design
nvm use
npm ci
npm run dev
```

Open http://localhost:8080. Run `npm run build` to generate `public/`.

The default configuration uses localhost and disables analytics. Set `configuration.baseUrl` to the intended domain before publishing. Only `content/` is published. Dependencies, build output, and Quartz caches are ignored by Git.

## Design status

The SDK APIs and authorization model are proposals. See `content/decisions.md` for agreed constraints and open decisions. The auth system has not been implemented or exercised against running ZITADEL and OpenFGA services.

## Upstream

Bootstrapped from Quartz 5 at commit `97a2d05f80c4c50534959b1d0d41cc4b3895625e`. Quartz source is vendored here with its MIT license in `LICENSE.txt`.

## Verification

Validated on November 7, 2026 using Node 24.13.1. The production build succeeds, all 18 Markdown pages have resolving internal links, and all 45 Mermaid diagrams rendered as SVG in the browser. The npm dependency audit reports zero vulnerabilities.

Dependency patches were applied after bootstrapping, including minimatch 11.0.5. The installed versions are pinned in `package-lock.json`.
