# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata build-base

# Install grpcurl
RUN go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# Set up workspace with correct relative paths
WORKDIR /src/frameworks/api_balancing

# Copy go.mod and go.sum first
COPY api_balancing/go.mod api_balancing/go.sum ./

# Copy shared pkg module (maintain relative path for replace directive)
COPY pkg/ ../pkg/

# Copy api_firehose for proto dependencies (maintain relative path for replace directive)
COPY api_firehose/ ../api_firehose/

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy service source code
COPY api_balancing/ ./

# Build the application with version and component injection
ARG VERSION=dev
ARG GIT_COMMIT=docker
ARG BUILD_DATE
ARG COMPONENT_NAME=foghorn
ARG COMPONENT_VERSION=0.0.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=linux go build \
    -ldflags "-X frameworks/pkg/version.Version=${VERSION} \
              -X frameworks/pkg/version.GitCommit=${GIT_COMMIT} \
              -X frameworks/pkg/version.BuildDate=${BUILD_DATE} \
              -X frameworks/pkg/version.ComponentName=${COMPONENT_NAME} \
              -X frameworks/pkg/version.ComponentVersion=${COMPONENT_VERSION}" \
    -o foghorn ./cmd/foghorn

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Copy grpcurl from builder
COPY --from=builder /go/bin/grpcurl /usr/local/bin/

# Create non-root user
RUN addgroup -g 1001 -S frameworks && \
    adduser -u 1001 -S frameworks -G frameworks

WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /src/frameworks/api_balancing/foghorn .

# Change ownership
RUN chown frameworks:frameworks /app/foghorn

USER frameworks

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD grpcurl -plaintext localhost:18008 grpc.health.v1.Health/Check || exit 1

EXPOSE 18008

CMD ["./foghorn"] 
