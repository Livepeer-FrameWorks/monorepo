# syntax=docker/dockerfile:1

ARG BUILD_ENV=production

FROM node:24-alpine AS builder

ARG BUILD_ENV
ARG VITE_AUTH_URL
ARG VITE_GRAPHQL_HTTP_URL
ARG VITE_GRAPHQL_WS_URL
ARG VITE_RTMP_DOMAIN
ARG VITE_HTTP_DOMAIN
ARG VITE_CDN_DOMAIN
ARG VITE_RTMP_PATH
ARG VITE_HLS_PATH
ARG VITE_WEBRTC_PATH
ARG VITE_EMBED_PATH
ARG VITE_MARKETING_SITE_URL
ARG BASE_PATH
ARG VITE_TURNSTILE_AUTH_SITE_KEY

ENV VITE_AUTH_URL=${VITE_AUTH_URL}
ENV VITE_GRAPHQL_HTTP_URL=${VITE_GRAPHQL_HTTP_URL}
ENV VITE_GRAPHQL_WS_URL=${VITE_GRAPHQL_WS_URL}
ENV VITE_RTMP_DOMAIN=${VITE_RTMP_DOMAIN}
ENV VITE_HTTP_DOMAIN=${VITE_HTTP_DOMAIN}
ENV VITE_CDN_DOMAIN=${VITE_CDN_DOMAIN}
ENV VITE_RTMP_PATH=${VITE_RTMP_PATH}
ENV VITE_HLS_PATH=${VITE_HLS_PATH}
ENV VITE_WEBRTC_PATH=${VITE_WEBRTC_PATH}
ENV VITE_EMBED_PATH=${VITE_EMBED_PATH}
ENV VITE_MARKETING_SITE_URL=${VITE_MARKETING_SITE_URL}
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
RUN if [ "$BUILD_ENV" = "development" ]; then \
      pnpm install --filter frameworks-frontend...; \
    else \
      pnpm install --filter frameworks-frontend... --frozen-lockfile; \
    fi

# copy sources
COPY npm_player/ npm_player/
COPY website_application/ website_application/

# build the app
RUN pnpm --filter frameworks-frontend... run build

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
