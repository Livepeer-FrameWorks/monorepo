FROM golang:1.27.0-alpine AS builder
RUN apk add --no-cache ca-certificates git
WORKDIR /src/frameworks/api_network_observer
COPY api_network_observer/go.mod api_network_observer/go.sum ./
COPY pkg/go.mod pkg/go.sum ../pkg/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY pkg/ ../pkg/
COPY api_network_observer/ ./
ARG CMD_PACKAGE=./cmd/network-observer
ARG COMPONENT_NAME=network-observer
ARG VERSION=dev
ARG GIT_COMMIT=docker
ARG BUILD_DATE
ARG COMPONENT_VERSION=0.0.0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
 CGO_ENABLED=0 GOOS=linux go build -ldflags "-X github.com/Livepeer-FrameWorks/monorepo/pkg/version.Version=${VERSION} -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.GitCommit=${GIT_COMMIT} -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.BuildDate=${BUILD_DATE} -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.ComponentName=${COMPONENT_NAME} -X github.com/Livepeer-FrameWorks/monorepo/pkg/version.ComponentVersion=${COMPONENT_VERSION}" -o /out/network ${CMD_PACKAGE}
FROM alpine:3.23
ARG HEALTH_PORT=18024
ENV HEALTH_PORT=${HEALTH_PORT}
RUN apk add --no-cache ca-certificates tzdata && addgroup -S frameworks && adduser -S -G frameworks frameworks && mkdir -p /var/lib/frameworks/network-prober && chown -R frameworks:frameworks /var/lib/frameworks
COPY --from=builder /out/network /usr/local/bin/network
USER frameworks
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -q --spider http://localhost:${HEALTH_PORT}/health || exit 1
EXPOSE 18024 18025
ENTRYPOINT ["/usr/local/bin/network"]
