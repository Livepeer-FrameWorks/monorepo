# Build stage
FROM golang:1.26.2-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set up workspace with correct relative paths
WORKDIR /src/frameworks/api_analytics_ingest

# Copy go.mod and go.sum first
COPY api_analytics_ingest/go.mod api_analytics_ingest/go.sum ./

# Copy shared pkg module go.mod and go.sum (maintain relative path for replace directive)
COPY pkg/go.mod pkg/go.sum ../pkg/

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy full shared pkg module source
COPY pkg/ ../pkg/

# Copy service source code
COPY api_analytics_ingest/ ./

# Build the application with version and component injection
ARG VERSION=dev
ARG GIT_COMMIT=docker
ARG BUILD_DATE
ARG COMPONENT_NAME=periscope-ingest
ARG COMPONENT_VERSION=0.0.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X frameworks/pkg/version.Version=${VERSION} \
              -X frameworks/pkg/version.GitCommit=${GIT_COMMIT} \
              -X frameworks/pkg/version.BuildDate=${BUILD_DATE} \
              -X frameworks/pkg/version.ComponentName=${COMPONENT_NAME} \
              -X frameworks/pkg/version.ComponentVersion=${COMPONENT_VERSION}" \
    -o periscope ./cmd/periscope

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1001 -S frameworks && \
    adduser -u 1001 -S frameworks -G frameworks

WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /src/frameworks/api_analytics_ingest/periscope .

# Change ownership
RUN chown frameworks:frameworks /app/periscope

USER frameworks

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget -q -O - http://localhost:18005/health >/dev/null || exit 1

EXPOSE 18005

CMD ["./periscope"] 
