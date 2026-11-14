# Build from the repository root with docker build -f docs/Dockerfile .
FROM node:24-slim AS build

RUN apt-get update && apt-get install -y git && rm -rf /var/lib/apt/lists/*
RUN npm install --global pnpm@12.10.1

WORKDIR /workspace
ENV CI=true
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY sdk/typescript/package.json ./sdk/typescript/package.json
COPY docs/package.json ./docs/package.json
COPY examples/ledger/package.json ./examples/ledger/package.json
COPY scripts/install-hooks.mjs ./scripts/install-hooks.mjs
RUN pnpm install --frozen-lockfile
COPY docs/ ./docs/
RUN pnpm run docs:build

FROM caddy:2-alpine
COPY docs/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /workspace/docs/public /srv
EXPOSE 8080
