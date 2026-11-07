# Build from the repository root with docker build -f docs/Dockerfile .
FROM node:24-slim

RUN apt-get update && apt-get install -y git && rm -rf /var/lib/apt/lists/*
RUN npm install --global pnpm@12.10.1

WORKDIR /workspace
ENV CI=true
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY sdk/typescript/package.json ./sdk/typescript/package.json
COPY docs/package.json ./docs/package.json
COPY scripts/install-hooks.mjs ./scripts/install-hooks.mjs
RUN pnpm install --frozen-lockfile
COPY docs/ ./docs/
CMD ["pnpm", "run", "docs:dev"]
