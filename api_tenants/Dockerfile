# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Install grpcurl
RUN go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# Set up workspace with correct relative paths
WORKDIR /src/frameworks/api_tenants

# Copy go.mod and go.sum first
COPY api_tenants/go.mod api_tenants/go.sum ./

# Copy shared pkg module (maintain relative path for replace directive)
COPY pkg/ ../pkg/

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy service source code
COPY api_tenants/ ./

# Build the application with version and component injection
ARG VERSION=dev
ARG GIT_COMMIT=docker
ARG BUILD_DATE
ARG COMPONENT_NAME=quartermaster
ARG COMPONENT_VERSION=0.0.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X frameworks/pkg/version.Version=${VERSION} \
              -X frameworks/pkg/version.GitCommit=${GIT_COMMIT} \
              -X frameworks/pkg/version.BuildDate=${BUILD_DATE} \
              -X frameworks/pkg/version.ComponentName=${COMPONENT_NAME} \
              -X frameworks/pkg/version.ComponentVersion=${COMPONENT_VERSION}" \
    -o quartermaster ./cmd/quartermaster

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
COPY --from=builder /src/frameworks/api_tenants/quartermaster .

# Change ownership
RUN chown frameworks:frameworks /app/quartermaster

USER frameworks

# Health check (using gRPC health endpoint)
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD grpcurl -plaintext localhost:${GRPC_PORT:-19002} grpc.health.v1.Health/Check || exit 1

EXPOSE 18002 19002

CMD ["./quartermaster"] 
