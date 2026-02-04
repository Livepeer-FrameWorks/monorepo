# syntax=docker/dockerfile:1

ARG BUILD_ENV=production

FROM node:24-alpine AS builder

ARG BUILD_ENV
ARG PUBLIC_DOCS_URL
ARG WEBAPP_PUBLIC_URL
ARG MARKETING_PUBLIC_URL
ARG GATEWAY_PUBLIC_URL
ARG STREAMING_INGEST_URL
ARG STREAMING_EDGE_URL
ARG STREAMING_HLS_PATH
ARG STREAMING_WEBRTC_PATH
ARG DISCORD_URL
ARG GITHUB_URL
ENV PUBLIC_DOCS_URL=${PUBLIC_DOCS_URL}
ENV WEBAPP_PUBLIC_URL=${WEBAPP_PUBLIC_URL}
ENV MARKETING_PUBLIC_URL=${MARKETING_PUBLIC_URL}
ENV GATEWAY_PUBLIC_URL=${GATEWAY_PUBLIC_URL}
ENV STREAMING_INGEST_URL=${STREAMING_INGEST_URL}
ENV STREAMING_EDGE_URL=${STREAMING_EDGE_URL}
ENV STREAMING_HLS_PATH=${STREAMING_HLS_PATH}
ENV STREAMING_WEBRTC_PATH=${STREAMING_WEBRTC_PATH}
ENV DISCORD_URL=${DISCORD_URL}
ENV GITHUB_URL=${GITHUB_URL}

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata \
  && corepack enable

# copy workspace manifests first for better layer caching
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
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
RUN cp docs/skills/skill.md website_docs/public/skill.md \
  && cp docs/skills/skill.json website_docs/public/skill.json
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
