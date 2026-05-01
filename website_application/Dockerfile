# syntax=docker/dockerfile:1

ARG BUILD_ENV=production

FROM node:24-alpine AS builder

ARG BUILD_ENV
ARG VITE_AUTH_URL
ARG VITE_GRAPHQL_HTTP_URL
ARG VITE_GRAPHQL_WS_URL
ARG VITE_STREAMING_INGEST_URL
ARG VITE_STREAMING_PLAY_URL
ARG VITE_STREAMING_EDGE_URL
ARG VITE_MARKETING_SITE_URL
ARG VITE_DOCS_SITE_URL
ARG VITE_APP_URL
ARG VITE_TURNSTILE_AUTH_SITE_KEY
ARG VITE_GATEWAY_URL

ENV VITE_AUTH_URL=${VITE_AUTH_URL}
ENV VITE_GRAPHQL_HTTP_URL=${VITE_GRAPHQL_HTTP_URL}
ENV VITE_GRAPHQL_WS_URL=${VITE_GRAPHQL_WS_URL}
ENV VITE_STREAMING_INGEST_URL=${VITE_STREAMING_INGEST_URL}
ENV VITE_STREAMING_PLAY_URL=${VITE_STREAMING_PLAY_URL}
ENV VITE_STREAMING_EDGE_URL=${VITE_STREAMING_EDGE_URL}
ENV VITE_MARKETING_SITE_URL=${VITE_MARKETING_SITE_URL}
ENV VITE_DOCS_SITE_URL=${VITE_DOCS_SITE_URL}
ENV VITE_APP_URL=${VITE_APP_URL}
ENV VITE_TURNSTILE_AUTH_SITE_KEY=${VITE_TURNSTILE_AUTH_SITE_KEY}
ENV VITE_GATEWAY_URL=${VITE_GATEWAY_URL}

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata \
  && corepack enable

# workspace manifests and config
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
COPY patches/ patches/

# npm_player workspace packages
COPY npm_player/package.json npm_player/
COPY npm_player/packages/core/package.json npm_player/packages/core/
COPY npm_player/packages/svelte/package.json npm_player/packages/svelte/
COPY npm_player/packages/react/package.json npm_player/packages/react/
COPY npm_player/packages/wc/package.json npm_player/packages/wc/
COPY npm_player/playground/package.json npm_player/playground/

# npm_studio workspace packages
COPY npm_studio/package.json npm_studio/
COPY npm_studio/packages/core/package.json npm_studio/packages/core/
COPY npm_studio/packages/svelte/package.json npm_studio/packages/svelte/
COPY npm_studio/packages/react/package.json npm_studio/packages/react/
COPY npm_studio/packages/wc/package.json npm_studio/packages/wc/

# shared workspace packages (site-config has no build step, so all source
# files must be present before install for injection to capture them)
COPY packages/site-config/ packages/site-config/

# remaining workspace members (pnpm requires all manifests for graph resolution)
COPY website_marketing/package.json website_marketing/
COPY website_docs/package.json website_docs/
COPY website_application/package.json website_application/

# install deps for the dashboard workspace
# --ignore-scripts: skip postinstall hooks like lefthook which require git
RUN --mount=type=cache,id=pnpm,target=/root/.local/share/pnpm/store \
    if [ "$BUILD_ENV" = "development" ]; then \
      pnpm install --filter frameworks-frontend... --ignore-scripts; \
    else \
      pnpm install --filter frameworks-frontend... --frozen-lockfile --ignore-scripts; \
    fi

# copy sources
COPY npm_player/ npm_player/
COPY npm_studio/ npm_studio/
COPY website_application/ website_application/
COPY docs/skills/ docs/skills/

# copy GraphQL schema and operations for Houdini codegen
COPY pkg/graphql/schema.graphql pkg/graphql/schema.graphql
COPY pkg/graphql/operations/ pkg/graphql/operations/

# ensure skill files are present in static assets
COPY scripts/copy-agent-files.sh scripts/copy-agent-files.sh
RUN cd website_application && ../scripts/copy-agent-files.sh static

# build the app (increase memory for Houdini codegen + Vite SSR + adapter-node)
RUN NODE_OPTIONS=--max-old-space-size=3072 pnpm --filter frameworks-frontend... run build

# deploy with production dependencies only into an isolated directory
RUN pnpm --filter frameworks-frontend --prod deploy /app/deployed

FROM node:24-alpine AS runner

ARG APP_PORT=18030
ENV PORT=${APP_PORT}
ENV HOST=0.0.0.0

WORKDIR /app

COPY --from=builder /app/deployed .

EXPOSE ${APP_PORT}

ENV NODE_ENV=production

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
  CMD nc -z -w 2 127.0.0.1 ${PORT:-18030} || exit 1

CMD ["node", "build/index.js"]
