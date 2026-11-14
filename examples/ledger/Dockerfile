FROM node:24-slim AS build
WORKDIR /workspace
ENV CI=true
RUN npm install --global pnpm@12.10.1
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY scripts/install-hooks.mjs ./scripts/install-hooks.mjs
COPY sdk/typescript/package.json ./sdk/typescript/package.json
COPY examples/ledger/package.json ./examples/ledger/package.json
RUN pnpm --filter @heimdall/ledger... --filter heimdall-workspace install --frozen-lockfile
COPY sdk/typescript/ ./sdk/typescript/
RUN pnpm --filter @techsavvyash/heimdall-sdk build
COPY examples/ledger/*.mjs ./examples/ledger/
RUN pnpm --filter @heimdall/ledger --prod deploy --legacy /app

FROM node:24-slim
ENV NODE_ENV=production
WORKDIR /workspace
COPY --from=build --chown=node:node /app ./
USER node
CMD ["node", "server.mjs"]
