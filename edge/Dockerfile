# syntax=docker/dockerfile:1

# Single-image edge node: helmsman + MistServer + Caddy under s6-overlay.
#
# The image consumes the SAME pinned native release tarballs (glibc) that
# bare-metal installs use — staged into dist/ by edge/stage-dist.sh — so the
# Foghorn-driven in-place component updates download byte-identical
# artifacts inside the container. Binaries live on the /opt/frameworks
# volume; the init-seed oneshot installs/upgrades them from
# /usr/share/frameworks/dist (see api_sidecar/internal/edgeseed).

FROM debian:bookworm-slim

ARG S6_OVERLAY_VERSION=3.2.3.0
ARG TARGETARCH

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates tzdata curl procps xz-utils util-linux \
 && rm -rf /var/lib/apt/lists/*

RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) S6_ARCH=x86_64; S6_SHA256=a93f02882c6ed46b21e7adb5c0add86154f01236c93cd82c7d682722e8840563 ;; \
      arm64) S6_ARCH=aarch64; S6_SHA256=0952056ff913482163cc30e35b2e944b507ba1025d78f5becbb89367bf344581 ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    NOARCH_SHA256=b720f9d9340efc8bb07528b9743813c836e4b02f8693d90241f047998b4c53cf; \
    curl -fsSLo /tmp/s6-overlay-noarch.tar.xz "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-noarch.tar.xz"; \
    echo "${NOARCH_SHA256}  /tmp/s6-overlay-noarch.tar.xz" | sha256sum -c -; \
    curl -fsSLo /tmp/s6-overlay-arch.tar.xz "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-${S6_ARCH}.tar.xz"; \
    echo "${S6_SHA256}  /tmp/s6-overlay-arch.tar.xz" | sha256sum -c -; \
    tar -C / -Jxpf /tmp/s6-overlay-noarch.tar.xz; \
    tar -C / -Jxpf /tmp/s6-overlay-arch.tar.xz; \
    rm -f /tmp/s6-overlay-*.tar.xz

# Native-parity accounts: frameworks (1001) runs helmsman + MistController,
# caddy runs the proxy; frameworks joins the caddy group for the shared
# cert/Caddyfile dirs (helmsman writes keys 0640 group caddy).
RUN groupadd --gid 1001 frameworks \
 && useradd --uid 1001 --gid frameworks --home-dir /var/lib/frameworks --create-home --shell /usr/sbin/nologin frameworks \
 && groupadd --system caddy \
 && useradd --system --gid caddy --home-dir /var/lib/caddy --shell /usr/sbin/nologin caddy \
 && usermod -aG caddy frameworks

COPY dist/ /usr/share/frameworks/dist/
COPY rootfs/ /

ENV DEPLOY_MODE=container \
    HELMSMAN_SUPERVISOR=s6 \
    CADDY_ADMIN_SOCKET=/run/caddy/admin.sock \
    CADDY_CONFIG_PATH=/etc/caddy/Caddyfile \
    CADDY_TLS_GROUP=caddy \
    MISTSERVER_URL=http://localhost:4242 \
    MISTSERVER_HTTP_URL=http://localhost:8080 \
    HELMSMAN_WEBHOOK_URL=http://localhost:18007 \
    HELMSMAN_STORAGE_LOCAL_PATH=/data/storage \
    S6_BEHAVIOUR_IF_STAGE2_FAILS=2 \
    S6_KEEP_ENV=1 \
    S6_CMD_WAIT_FOR_SERVICES_MAXTIME=0

# 18007 (helmsman) deliberately NOT exposed: it carries unauthenticated
# local endpoints (/node/mode) and is container-internal by contract.
# Remote consumers of the artifact relay (peer helmsman pulls) come in
# through Caddy's @artifact_relay route on 443, never 18007 directly —
# advertising it would make `docker run -P` publish it.
EXPOSE 80 443 1935 4200 5554 8080 8889/udp 18203/udp

VOLUME ["/opt/frameworks", "/etc/frameworks", "/etc/caddy", "/var/lib/caddy", "/data/storage"]

# Helmsman serves HTTP (gin) on 18007; /health is the platform-wide health
# route. edge status treats "unhealthy" as failed, so this must match the
# real listener.
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD curl -fsS http://127.0.0.1:18007/health || exit 1

ENTRYPOINT ["/init"]
