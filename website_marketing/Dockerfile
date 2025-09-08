# Build stage
FROM node:18-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Copy package files
COPY package*.json ./
# Use ci when lock matches; fall back to install during local dev
RUN npm ci || npm install

# Copy source code
COPY . .

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
