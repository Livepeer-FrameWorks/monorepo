# Build argument to control player source
ARG BUILD_ENV=production

# Player builder stage (only used in development)
FROM node:18-alpine AS player-builder
# Only build if we're in development mode
ARG BUILD_ENV
RUN if [ "$BUILD_ENV" = "development" ]; then \
      echo "Building local player package..."; \
    else \
      echo "Skipping local player build (using NPM package)"; \
    fi

WORKDIR /player
# Copy and build player only in dev mode
COPY npm_player/package*.json ./
RUN if [ "$BUILD_ENV" = "development" ]; then \
      npm ci && \
      echo "Player dependencies installed"; \
    fi
COPY npm_player/ ./
RUN if [ "$BUILD_ENV" = "development" ]; then \
      npm run build && \
      echo "Player built successfully"; \
    fi

# Build stage
FROM node:18-alpine AS builder

WORKDIR /app

# Copy package files
COPY website_application/package*.json ./

# Install dependencies - use BUILD_ENV to determine behavior
ARG BUILD_ENV
RUN if [ "$BUILD_ENV" = "development" ]; then \
      echo "Development mode: installing all dependencies" && \
      npm install; \
    else \
      echo "Production mode: using npm ci" && \
      npm ci; \
    fi

# Copy built player from player-builder stage (only in dev)
COPY --from=player-builder /player/dist* /tmp/player-dist/
RUN if [ "$BUILD_ENV" = "development" ] && [ -d "/tmp/player-dist/dist" ]; then \
      echo "Copying local player build..." && \
      rm -rf node_modules/@livepeer-frameworks/player/dist && \
      mkdir -p node_modules/@livepeer-frameworks/player && \
      cp -r /tmp/player-dist/dist node_modules/@livepeer-frameworks/player/ && \
      if [ -f "/tmp/player-dist/package.json" ]; then \
        cp /tmp/player-dist/package.json node_modules/@livepeer-frameworks/player/; \
      fi && \
      echo "Local player copied successfully"; \
    else \
      echo "Using NPM package for player"; \
    fi

# Copy source code
COPY website_application/ .

# Pass environment variables to build process
ARG VITE_AUTH_URL
ARG VITE_GRAPHQL_HTTP_URL
ARG VITE_GRAPHQL_WS_URL
ARG VITE_ANALYTICS_API_URL
ARG VITE_INGEST_DOMAIN
ARG VITE_CDN_DOMAIN
ARG VITE_RTMP_PATH
ARG VITE_HLS_PATH
ARG VITE_EMBED_PATH
ARG VITE_MARKETING_SITE_URL
ARG BASE_PATH

ENV VITE_AUTH_URL=${VITE_AUTH_URL}
ENV VITE_GRAPHQL_HTTP_URL=${VITE_GRAPHQL_HTTP_URL}
ENV VITE_GRAPHQL_WS_URL=${VITE_GRAPHQL_WS_URL}
ENV VITE_ANALYTICS_API_URL=${VITE_ANALYTICS_API_URL}
ENV VITE_INGEST_DOMAIN=${VITE_INGEST_DOMAIN}
ENV VITE_CDN_DOMAIN=${VITE_CDN_DOMAIN}
ENV VITE_RTMP_PATH=${VITE_RTMP_PATH}
ENV VITE_HLS_PATH=${VITE_HLS_PATH}
ENV VITE_EMBED_PATH=${VITE_EMBED_PATH}
ENV VITE_MARKETING_SITE_URL=${VITE_MARKETING_SITE_URL}
ENV BASE_PATH=${BASE_PATH}

# Build the app
RUN npm run build

# Production stage
FROM node:18-alpine AS runner

WORKDIR /app

# Copy package files
COPY website_application/package*.json ./

# Install only production dependencies
RUN npm ci --only=production && npm cache clean --force

# Copy built app from builder stage
COPY --from=builder /app/build ./build
COPY --from=builder /app/static ./static

EXPOSE 18030

ENV NODE_ENV=production

CMD ["node", "build/index.js"]