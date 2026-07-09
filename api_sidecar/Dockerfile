# Build stage
FROM golang:1.26.2-alpine AS builder

ENV GOFLAGS="-tags=nomsgpack"

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata build-base

# Set up workspace with correct relative paths
WORKDIR /src/frameworks/api_sidecar

# Copy go.mod and go.sum first
COPY api_sidecar/go.mod api_sidecar/go.sum ./

# Copy shared pkg module go.mod and go.sum (maintain relative path for replace directive)
COPY pkg/go.mod pkg/go.sum ../pkg/

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy full shared pkg module source
COPY pkg/ ../pkg/

# Copy service source code
COPY api_sidecar/ ./

# Build the application with version and component injection
ARG VERSION=dev
ARG GIT_COMMIT=docker
ARG BUILD_DATE
ARG COMPONENT_NAME=helmsman
ARG COMPONENT_VERSION=0.0.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/Livepeer-FrameWorks/monorepo/pkg/version.Version=${VERSION} \
              -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.GitCommit=${GIT_COMMIT} \
              -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.BuildDate=${BUILD_DATE} \
              -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.ComponentName=${COMPONENT_NAME} \
              -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.ComponentVersion=${COMPONENT_VERSION}" \
    -o helmsman ./cmd/helmsman

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1001 -S frameworks && \
    adduser -u 1001 -S frameworks -G frameworks

WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /src/frameworks/api_sidecar/helmsman .

# Change ownership
RUN chown frameworks:frameworks /app/helmsman

USER frameworks

# Health check: helmsman serves HTTP (gin) on 18007, not gRPC.
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:18007/health || exit 1

EXPOSE 18007

CMD ["./helmsman"] 
