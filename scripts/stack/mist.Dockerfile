# syntax=docker/dockerfile:1
# Builds MistServer from a local checkout into the native release layout
# (bin/, lib/, share/) that edge/stage-dist.sh stages, for the stack's edge
# image (EDGE_DEV_MIST_TAR). Mirrors the fork's native linux build
# (.github/workflows/build.yml "Build Native Binaries"): Ubuntu 24.04, AV
# processing on, pinned MbedTLS/usrsctp/libav/x264 fallbacks, RIST off. ONNX is
# off, and dav1d/SVT-AV1 come from Ubuntu instead of the fork's static builds:
# the ffmpeg subproject requires both, the stack does not exercise AV1, and
# those builds dominate the first-run time.
#
# The source tree, extracted subprojects and build directory live in BuildKit
# cache mounts, so a rerun recompiles only what changed since the last build.

FROM ubuntu:24.04 AS build
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential ca-certificates cmake git nasm ninja-build pkg-config python3 python3-pip rsync yasm \
      libaom-dev libcjson-dev libdav1d-dev libjpeg-dev liblzma-dev libmp3lame-dev libnuma-dev libopus-dev libpng-dev libsvtav1enc-dev \
      libsrt-openssl-dev libsrtp2-dev libvpx-dev libx265-dev libxml2-dev zlib1g-dev \
 && python3 -m pip install --break-system-packages 'meson>=1.6.0' \
 && rm -rf /var/lib/apt/lists/*

COPY source/ /src/
ARG MIST_SOURCE_REVISION=unknown
RUN --mount=type=cache,id=fw-stack-mist-work,target=/work,sharing=locked \
    set -eu; \
    mkdir -p /work/src; \
    rsync -a --delete --filter='P /subprojects/*/' /src/ /work/src/; \
    if [ ! -f /work/build/build.ninja ]; then \
      meson setup /work/build /work/src \
        --default-library=static --wrap-mode=default --prefer-static \
        --force-fallback-for=mbedtls,usrsctp,libavcodec,libavfilter,libavformat,libavutil,libswscale,libswresample,x264 \
        -DWITH_AV=true -DONNX=disabled -DNORIST=true -DNOUPDATE=true -DDEBUG=3 \
        -DVERSION=${MIST_SOURCE_REVISION} -DRELEASE=FrameWorks_stack \
        --prefix=/opt/mist-install; \
    else \
      meson configure /work/build -DVERSION=${MIST_SOURCE_REVISION}; \
    fi; \
    ninja -C /work/build; \
    rm -rf /opt/mist-install; \
    meson install -C /work/build --no-rebuild >/dev/null; \
    test -x /opt/mist-install/bin/MistController; \
    test -x /opt/mist-install/bin/MistProcAV; \
    test -x /opt/mist-install/bin/MistProcThumbs

# Same staging as scripts/verify-mist-current-source.sh: carry every shared
# library the binaries load from outside the C runtime in lib/.
RUN set -eu; \
    stage=/stage; mkdir -p "$stage/bin" "$stage/lib" "$stage/share"; \
    cp -a /opt/mist-install/bin/. "$stage/bin/"; \
    if [ -d /opt/mist-install/lib ]; then cp -a /opt/mist-install/lib/. "$stage/lib/"; fi; \
    if [ -d /opt/mist-install/share ]; then cp -a /opt/mist-install/share/. "$stage/share/"; fi; \
    for bin in "$stage"/bin/*; do ldd "$bin" 2>/dev/null || true; done \
      | awk '$2 == "=>" && $3 ~ /^\// { print $3 }' | sort -u \
      | while read -r lib; do \
          name=${lib##*/}; \
          case "$name" in libc.so.* | libm.so.* | libdl.so.* | libpthread.so.* | librt.so.* | ld-linux*) continue ;; esac; \
          [ -e "$stage/lib/$name" ] || cp -L "$lib" "$stage/lib/$name"; \
        done; \
    mkdir -p /out; tar -C "$stage" -czf /out/mist.tar.gz .

FROM scratch AS out
COPY --from=build /out/mist.tar.gz /mist.tar.gz
