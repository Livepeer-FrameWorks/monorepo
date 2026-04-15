# Build stage
FROM golang:1.26.2-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src/frameworks/api_assets

COPY api_assets/go.mod api_assets/go.sum ./
COPY pkg/go.mod pkg/go.sum ../pkg/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY pkg/ ../pkg/
COPY api_assets/ ./

ARG VERSION=dev
ARG GIT_COMMIT=docker
ARG BUILD_DATE
ARG COMPONENT_NAME=chandler
ARG COMPONENT_VERSION=0.0.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X frameworks/pkg/version.Version=${VERSION} \
              -X frameworks/pkg/version.GitCommit=${GIT_COMMIT} \
              -X frameworks/pkg/version.BuildDate=${BUILD_DATE} \
              -X frameworks/pkg/version.ComponentName=${COMPONENT_NAME} \
              -X frameworks/pkg/version.ComponentVersion=${COMPONENT_VERSION}" \
    -o chandler ./cmd/chandler

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -g 1001 -S frameworks && \
    adduser -u 1001 -S frameworks -G frameworks

WORKDIR /app

COPY --from=builder /src/frameworks/api_assets/chandler .
RUN chown frameworks:frameworks /app/chandler

USER frameworks

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:${PORT:-18020}/health || exit 1

EXPOSE 18020

CMD ["./chandler"]
