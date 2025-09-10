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

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Copy package files
COPY website_marketing/package*.json ./

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
COPY website_marketing/ .

# Pass environment variables to build process
ARG VITE_APP_URL
ARG VITE_API_URL
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

ENV VITE_APP_URL=${VITE_APP_URL}
ENV VITE_API_URL=${VITE_API_URL}
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

# Build the app
RUN npm run build

# Production stage
FROM node:18-alpine

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1001 -S appuser && \
    adduser -u 1001 -S appuser -G appuser

WORKDIR /app

# Install serve for production static file serving
RUN npm install -g serve

# Copy built app from builder stage
COPY --from=builder /app/dist ./dist

# Change ownership
RUN chown -R appuser:appuser /app

USER appuser

EXPOSE 18031

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:9009/health || exit 1

CMD ["serve", "-s", "dist", "-l", "9009"]