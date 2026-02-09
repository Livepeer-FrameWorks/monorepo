# syntax=docker/dockerfile:1

ARG BUILD_ENV=production

FROM node:24-alpine AS builder

ARG BUILD_ENV
ARG VITE_APP_URL
ARG VITE_GITHUB_URL
ARG VITE_LIVEPEER_URL
ARG VITE_LIVEPEER_EXPLORER_URL
ARG VITE_CONTACT_EMAIL
ARG VITE_FORUM_URL
ARG VITE_DISCORD_URL
ARG VITE_DEMO_STREAM_NAME
ARG VITE_COMPANY_NAME
ARG VITE_DOMAIN
ARG VITE_GATEWAY_URL
ARG VITE_CONTACT_API_URL
ARG VITE_TURNSTILE_FORMS_SITE_KEY
ARG VITE_DOCS_SITE_URL

ENV VITE_APP_URL=${VITE_APP_URL}
ENV VITE_GITHUB_URL=${VITE_GITHUB_URL}
ENV VITE_LIVEPEER_URL=${VITE_LIVEPEER_URL}
ENV VITE_LIVEPEER_EXPLORER_URL=${VITE_LIVEPEER_EXPLORER_URL}
ENV VITE_CONTACT_EMAIL=${VITE_CONTACT_EMAIL}
ENV VITE_FORUM_URL=${VITE_FORUM_URL}
ENV VITE_DISCORD_URL=${VITE_DISCORD_URL}
ENV VITE_DEMO_STREAM_NAME=${VITE_DEMO_STREAM_NAME}
ENV VITE_COMPANY_NAME=${VITE_COMPANY_NAME}
ENV VITE_DOMAIN=${VITE_DOMAIN}
ENV VITE_GATEWAY_URL=${VITE_GATEWAY_URL}
ENV VITE_CONTACT_API_URL=${VITE_CONTACT_API_URL}
ENV VITE_TURNSTILE_FORMS_SITE_KEY=${VITE_TURNSTILE_FORMS_SITE_KEY}
ENV VITE_DOCS_SITE_URL=${VITE_DOCS_SITE_URL}

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata \
  && corepack enable

# copy workspace manifests first for better layer caching
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY patches/ patches/
COPY npm_player/package.json npm_player/
COPY npm_player/packages/core/package.json npm_player/packages/core/
COPY npm_player/packages/react/package.json npm_player/packages/react/
COPY npm_player/packages/svelte/package.json npm_player/packages/svelte/
COPY npm_player/playground/package.json npm_player/playground/
COPY npm_studio/package.json npm_studio/
COPY npm_studio/packages/core/package.json npm_studio/packages/core/
COPY npm_studio/packages/react/package.json npm_studio/packages/react/
COPY npm_studio/packages/svelte/package.json npm_studio/packages/svelte/
COPY website_marketing/package.json website_marketing/

# install dependencies for the marketing workspace (dev deps required for build)
# --ignore-scripts: skip postinstall hooks like lefthook which require git
RUN if [ "$BUILD_ENV" = "development" ]; then \
      pnpm install --filter frameworks-website... --ignore-scripts; \
    else \
      pnpm install --filter frameworks-website... --frozen-lockfile --ignore-scripts; \
    fi

# copy the remaining sources now that deps are cached
COPY npm_player/ npm_player/
COPY website_marketing/ website_marketing/
COPY docs/skills/ docs/skills/

# build the marketing site
COPY scripts/copy-agent-files.sh scripts/copy-agent-files.sh
RUN cd website_marketing && ../scripts/copy-agent-files.sh public
RUN pnpm --filter frameworks-website... run build

FROM node:24-alpine AS runner

ARG APP_PORT=18031
ENV PORT=${APP_PORT}
ENV HOST=0.0.0.0

RUN apk add --no-cache ca-certificates tzdata \
  && npm install -g serve \
  && addgroup -g 1001 -S appuser \
  && adduser -u 1001 -S appuser -G appuser

WORKDIR /app

COPY --from=builder /app/website_marketing/dist ./dist

RUN chown -R appuser:appuser /app
USER appuser

EXPOSE ${APP_PORT}

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:${PORT:-18031}/health || exit 1

CMD ["sh", "-c", "serve -s dist -l ${PORT:-18031}"]
