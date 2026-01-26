# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS builder

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

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X frameworks/pkg/version.Version=${VERSION:-dev} -X frameworks/pkg/version.GitCommit=${GIT_COMMIT:-unknown}" \
    -o /deckhand ./cmd/deckhand

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata wget

WORKDIR /app

COPY --from=builder /deckhand /app/deckhand

RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser && \
    chown -R appuser:appuser /app

USER appuser

EXPOSE 18015

CMD ["/app/deckhand"]
