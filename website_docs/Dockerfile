# syntax=docker/dockerfile:1

ARG BUILD_ENV=production

FROM node:24-alpine AS builder

ARG BUILD_ENV
# Reuse VITE_* vars from webapp/marketing - no duplication
ARG VITE_DOCS_SITE_URL
ARG VITE_APP_URL
ARG VITE_MARKETING_SITE_URL
ARG VITE_GATEWAY_URL
ARG VITE_STREAMING_INGEST_URL
ARG VITE_STREAMING_EDGE_URL
ARG VITE_STREAMING_PLAY_URL
ARG VITE_STREAMING_RTMP_PORT
ARG VITE_STREAMING_SRT_PORT
ARG VITE_STREAMING_RTMP_PATH
ARG VITE_STREAMING_HLS_PATH
ARG VITE_STREAMING_WEBRTC_PATH
ARG VITE_DISCORD_URL
ARG VITE_GITHUB_URL
ARG VITE_TURNSTILE_AUTH_SITE_KEY
ENV VITE_DOCS_SITE_URL=${VITE_DOCS_SITE_URL}
ENV VITE_APP_URL=${VITE_APP_URL}
ENV VITE_MARKETING_SITE_URL=${VITE_MARKETING_SITE_URL}
ENV VITE_GATEWAY_URL=${VITE_GATEWAY_URL}
ENV VITE_STREAMING_INGEST_URL=${VITE_STREAMING_INGEST_URL}
ENV VITE_STREAMING_EDGE_URL=${VITE_STREAMING_EDGE_URL}
ENV VITE_STREAMING_PLAY_URL=${VITE_STREAMING_PLAY_URL}
ENV VITE_STREAMING_RTMP_PORT=${VITE_STREAMING_RTMP_PORT}
ENV VITE_STREAMING_SRT_PORT=${VITE_STREAMING_SRT_PORT}
ENV VITE_STREAMING_RTMP_PATH=${VITE_STREAMING_RTMP_PATH}
ENV VITE_STREAMING_HLS_PATH=${VITE_STREAMING_HLS_PATH}
ENV VITE_STREAMING_WEBRTC_PATH=${VITE_STREAMING_WEBRTC_PATH}
ENV VITE_DISCORD_URL=${VITE_DISCORD_URL}
ENV VITE_GITHUB_URL=${VITE_GITHUB_URL}
ENV VITE_TURNSTILE_AUTH_SITE_KEY=${VITE_TURNSTILE_AUTH_SITE_KEY}

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata \
  && corepack enable

# copy workspace manifests first for better layer caching
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY patches/ patches/
COPY website_docs/package.json website_docs/

# install dependencies for the docs workspace (dev deps required for build)
# --ignore-scripts: skip postinstall hooks like lefthook which require git
RUN if [ "$BUILD_ENV" = "development" ]; then \
      pnpm install --filter website_docs... --ignore-scripts; \
    else \
      pnpm install --filter website_docs... --frozen-lockfile --ignore-scripts; \
    fi

# copy the remaining sources now that deps are cached
COPY website_docs/ website_docs/
COPY docs/skills/ docs/skills/

# build the docs site
COPY scripts/copy-agent-files.sh scripts/copy-agent-files.sh
RUN cd website_docs && ../scripts/copy-agent-files.sh public
RUN pnpm --filter website_docs... run build

FROM node:24-alpine AS runner

ARG APP_PORT=18033
ENV PORT=${APP_PORT}
ENV HOST=0.0.0.0

RUN apk add --no-cache ca-certificates tzdata \
  && npm install -g serve \
  && addgroup -g 1001 -S appuser \
  && adduser -u 1001 -S appuser -G appuser

WORKDIR /app

COPY --from=builder /app/website_docs/dist ./dist

RUN chown -R appuser:appuser /app
USER appuser

EXPOSE ${APP_PORT}

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:${PORT:-18033}/ || exit 1

CMD ["sh", "-c", "serve dist -l ${PORT:-18033}"]
