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
ARG VITE_STREAMING_RTMP_PORT
ARG VITE_STREAMING_SRT_PORT
ARG VITE_STREAMING_RTMP_PATH
ARG VITE_STREAMING_HLS_PATH
ARG VITE_STREAMING_WEBRTC_PATH
ARG VITE_STREAMING_EMBED_PATH
ARG VITE_MARKETING_SITE_URL
ARG VITE_DOCS_SITE_URL
ARG BASE_PATH
ARG VITE_TURNSTILE_AUTH_SITE_KEY

ENV VITE_AUTH_URL=${VITE_AUTH_URL}
ENV VITE_GRAPHQL_HTTP_URL=${VITE_GRAPHQL_HTTP_URL}
ENV VITE_GRAPHQL_WS_URL=${VITE_GRAPHQL_WS_URL}
ENV VITE_STREAMING_INGEST_URL=${VITE_STREAMING_INGEST_URL}
ENV VITE_STREAMING_PLAY_URL=${VITE_STREAMING_PLAY_URL}
ENV VITE_STREAMING_EDGE_URL=${VITE_STREAMING_EDGE_URL}
ENV VITE_STREAMING_RTMP_PORT=${VITE_STREAMING_RTMP_PORT}
ENV VITE_STREAMING_SRT_PORT=${VITE_STREAMING_SRT_PORT}
ENV VITE_STREAMING_RTMP_PATH=${VITE_STREAMING_RTMP_PATH}
ENV VITE_STREAMING_HLS_PATH=${VITE_STREAMING_HLS_PATH}
ENV VITE_STREAMING_WEBRTC_PATH=${VITE_STREAMING_WEBRTC_PATH}
ENV VITE_STREAMING_EMBED_PATH=${VITE_STREAMING_EMBED_PATH}
ENV VITE_MARKETING_SITE_URL=${VITE_MARKETING_SITE_URL}
ENV VITE_DOCS_SITE_URL=${VITE_DOCS_SITE_URL}
ENV BASE_PATH=${BASE_PATH}
ENV VITE_TURNSTILE_AUTH_SITE_KEY=${VITE_TURNSTILE_AUTH_SITE_KEY}

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata \
  && corepack enable

# workspace manifests
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./

# npm_player workspace packages
COPY npm_player/package.json npm_player/
COPY npm_player/packages/core/package.json npm_player/packages/core/
COPY npm_player/packages/svelte/package.json npm_player/packages/svelte/
COPY npm_player/packages/react/package.json npm_player/packages/react/

# npm_studio workspace packages
COPY npm_studio/package.json npm_studio/
COPY npm_studio/packages/core/package.json npm_studio/packages/core/
COPY npm_studio/packages/svelte/package.json npm_studio/packages/svelte/
COPY npm_studio/packages/react/package.json npm_studio/packages/react/

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
RUN cp docs/skills/skill.md website_application/static/skill.md \
  && cp docs/skills/skill.json website_application/static/skill.json \
  && cp docs/skills/llms.txt website_application/static/llms.txt \
  && cp docs/skills/robots.txt website_application/static/robots.txt \
  && mkdir -p website_application/static/.well-known \
  && cp docs/skills/mcp.json website_application/static/.well-known/mcp.json \
  && cp docs/skills/security.txt website_application/static/.well-known/security.txt \
  && cp docs/skills/did.json website_application/static/.well-known/did.json

# build the app (increase memory for Houdini codegen + Vite SSR + adapter-node)
RUN NODE_OPTIONS=--max-old-space-size=4096 pnpm --filter frameworks-frontend... run build

# prune to production dependencies only
RUN pnpm prune --prod

FROM node:24-alpine AS runner

ARG APP_PORT=18030
ENV PORT=${APP_PORT}
ENV HOST=0.0.0.0

WORKDIR /app

# copy workspace manifests and pruned node_modules
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY npm_player/package.json npm_player/
COPY npm_studio/package.json npm_studio/
COPY website_application/package.json website_application/
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/website_application/node_modules ./website_application/node_modules
COPY --from=builder /app/npm_player ./npm_player
COPY --from=builder /app/npm_studio ./npm_studio

# copy build artifacts
COPY --from=builder /app/website_application/build ./website_application/build
COPY --from=builder /app/website_application/static ./website_application/static

WORKDIR /app/website_application

EXPOSE ${APP_PORT}

ENV NODE_ENV=production

CMD ["node", "build/index.js"]
