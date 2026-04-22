# syntax=docker/dockerfile:1

FROM golang:1.26.2-alpine AS builder

WORKDIR /workspace

RUN apk add --no-cache git ca-certificates

COPY pkg/go.mod pkg/go.sum pkg/
COPY api_ticketing/go.mod api_ticketing/go.sum api_ticketing/

WORKDIR /workspace/api_ticketing

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

WORKDIR /workspace
COPY pkg/ pkg/

WORKDIR /workspace
COPY api_ticketing/ api_ticketing/

WORKDIR /workspace/api_ticketing

ARG VERSION=dev
ARG GIT_COMMIT=docker
ARG BUILD_DATE
ARG COMPONENT_NAME=deckhand
ARG COMPONENT_VERSION=0.0.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X frameworks/pkg/version.Version=${VERSION} \
              -X frameworks/pkg/version.GitCommit=${GIT_COMMIT} \
              -X frameworks/pkg/version.BuildDate=${BUILD_DATE} \
              -X frameworks/pkg/version.ComponentName=${COMPONENT_NAME} \
              -X frameworks/pkg/version.ComponentVersion=${COMPONENT_VERSION}" \
    -o /deckhand ./cmd/deckhand

FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata wget

WORKDIR /app

COPY --from=builder /deckhand /app/deckhand

RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser && \
    chown -R appuser:appuser /app

USER appuser

EXPOSE 18015

CMD ["/app/deckhand"]
