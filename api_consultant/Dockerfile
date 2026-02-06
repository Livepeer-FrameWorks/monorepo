# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set up workspace with correct relative paths
WORKDIR /src/frameworks/api_consultant

# Copy go.mod and go.sum first
COPY api_consultant/go.mod api_consultant/go.sum ./

# Copy shared pkg module go.mod and go.sum (maintain relative path for replace directive)
COPY pkg/go.mod pkg/go.sum ../pkg/

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy full shared pkg module source
COPY pkg/ ../pkg/

# Copy service source code
COPY api_consultant/ ./

# Build the application with version and component injection
ARG VERSION=dev
ARG GIT_COMMIT=docker
ARG BUILD_DATE
ARG COMPONENT_NAME=skipper
ARG COMPONENT_VERSION=0.0.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X frameworks/pkg/version.Version=${VERSION} \
              -X frameworks/pkg/version.GitCommit=${GIT_COMMIT} \
              -X frameworks/pkg/version.BuildDate=${BUILD_DATE} \
              -X frameworks/pkg/version.ComponentName=${COMPONENT_NAME} \
              -X frameworks/pkg/version.ComponentVersion=${COMPONENT_VERSION}" \
    -o skipper ./cmd/skipper

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata curl

# Create non-root user
RUN addgroup -g 1001 -S frameworks && \
    adduser -u 1001 -S frameworks -G frameworks

WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /src/frameworks/api_consultant/skipper .

# Change ownership
RUN chown frameworks:frameworks /app/skipper

USER frameworks

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:18018/health || exit 1

EXPOSE 18018 19007

CMD ["./skipper"]
