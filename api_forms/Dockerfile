# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS builder

WORKDIR /workspace

RUN apk add --no-cache git ca-certificates

COPY pkg/ pkg/
COPY api_forms/ api_forms/

WORKDIR /workspace/api_forms

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -ldflags="-s -w -X frameworks/pkg/version.Version=${VERSION:-dev} -X frameworks/pkg/version.GitCommit=${GIT_COMMIT:-unknown}" \
    -o /forms ./cmd/forms

FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /forms /app/forms

RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser && \
    chown -R appuser:appuser /app

USER appuser

EXPOSE 18032

CMD ["/app/forms"]
