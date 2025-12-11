# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Install grpcurl
RUN go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

WORKDIR /src/frameworks

# Copy go.mod and go.sum first
COPY api_firehose/go.mod api_firehose/go.sum api_firehose/

# Copy shared pkg module (maintain relative path for replace directive)
COPY pkg/ pkg/

# Download dependencies
WORKDIR /src/frameworks/api_firehose
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Copy service source code
WORKDIR /src/frameworks
COPY api_firehose/ api_firehose/

# Build the application with version and component injection
WORKDIR /src/frameworks/api_firehose
ARG VERSION=dev
ARG GIT_COMMIT=docker
ARG BUILD_DATE
ARG COMPONENT_NAME=decklog
ARG COMPONENT_VERSION=0.0.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-X frameworks/pkg/version.Version=${VERSION} -X frameworks/pkg/version.GitCommit=${GIT_COMMIT} -X frameworks/pkg/version.BuildDate=${BUILD_DATE} -X frameworks/pkg/version.ComponentName=${COMPONENT_NAME} -X frameworks/pkg/version.ComponentVersion=${COMPONENT_VERSION}" -o bin/decklog cmd/decklog/main.go

# Final stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Copy grpcurl from builder
COPY --from=builder /go/bin/grpcurl /usr/local/bin/

# Create non-root user
RUN addgroup -g 1001 -S frameworks && \
    adduser -u 1001 -S frameworks -G frameworks

WORKDIR /app

# Copy binary only
COPY --from=builder /src/frameworks/api_firehose/bin/decklog .

# Set ownership
RUN chown -R frameworks:frameworks /app

# Switch to non-root user
USER frameworks

# Expose gRPC port
EXPOSE 18006

ENTRYPOINT ["/app/decklog"] 
