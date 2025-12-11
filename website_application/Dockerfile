# syntax=docker/dockerfile:1

ARG BUILD_ENV=production

FROM node:24-alpine AS builder

ARG BUILD_ENV
ARG VITE_AUTH_URL
ARG VITE_GRAPHQL_HTTP_URL
ARG VITE_GRAPHQL_WS_URL
ARG VITE_STREAMING_INGEST_URL
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
COPY npm_player/package.json npm_player/
COPY website_application/package.json website_application/

# install deps for the dashboard workspace
RUN --mount=type=cache,id=pnpm,target=/root/.local/share/pnpm/store \
    if [ "$BUILD_ENV" = "development" ]; then \
      pnpm install --filter frameworks-frontend...; \
    else \
      pnpm install --filter frameworks-frontend... --frozen-lockfile; \
    fi

# copy sources
COPY npm_player/ npm_player/
COPY website_application/ website_application/

# copy GraphQL schema for Houdini codegen
COPY pkg/graphql/schema.graphql pkg/graphql/schema.graphql

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
COPY website_application/package.json website_application/
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/website_application/node_modules ./website_application/node_modules
COPY --from=builder /app/npm_player ./npm_player

# copy build artifacts
COPY --from=builder /app/website_application/build ./website_application/build
COPY --from=builder /app/website_application/static ./website_application/static

WORKDIR /app/website_application

EXPOSE ${APP_PORT}

ENV NODE_ENV=production

CMD ["node", "build/index.js"]
