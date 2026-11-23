# Scenario runner for the stack: scenarios run inside the stack network and
# address services by name, so slots need no published host ports. It carries
# the SDK toolchains (Node for npm_api's built dist, Python with the
# livepeer_frameworks runtime dependencies, Go for sdk_go) and the Docker CLI
# for fault injection into its own compose project (scripts/stack/lib.sh).
# FFmpeg 8.0 is the first release whose FLV muxer writes Enhanced RTMP v2
# multitrack video; Debian's 5.1 cannot publish the multitrack ingest scenarios.
FROM mwader/static-ffmpeg:8.1.2@sha256:33f770f812cbfc3de96c547157fc9faf8bd95a36481753439ffa761045167585 AS ffmpeg
FROM golang:1.27-bookworm AS go
FROM docker:28-cli AS docker
FROM docker/compose-bin:v2.39.4 AS compose

FROM node:24-bookworm-slim
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash ca-certificates curl gcc git jq libc6-dev openssl postgresql-client procps python3 python3-venv util-linux \
  && rm -rf /var/lib/apt/lists/*

COPY --from=go /usr/local/go /usr/local/go
COPY --from=ffmpeg /ffmpeg /ffprobe /usr/local/bin/
COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
COPY --from=compose /docker-compose /usr/local/lib/docker/cli-plugins/docker-compose
ENV PATH=/opt/venv/bin:/usr/local/go/bin:/root/go/bin:$PATH \
    GOTOOLCHAIN=local

# livepeer_frameworks' runtime dependencies (sdk_python/pyproject.toml); the SDK
# itself runs from the mounted checkout via PYTHONPATH.
RUN python3 -m venv /opt/venv \
  && /opt/venv/bin/pip install --no-cache-dir \
    'graphql-core>=3.2,<3.3' 'httpx>=0.27,<1' 'pydantic>=2.7,<3' 'websockets>=13,<18' \
    'standardwebhooks>=1.1.0,<2' 'betterproto2==0.10.0' 'cryptography>=43'

WORKDIR /repo
ENTRYPOINT ["bash"]
CMD ["-c", "sleep infinity"]
