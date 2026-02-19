#!/bin/bash
set -euo pipefail

# WASM Decoder Build Script
#
# Downloads codec libraries, compiles each to a static .a,
# then links with our thin C wrapper to produce standalone .wasm files.
#
# Output directory: /out (mount via docker -v)

OUT="${1:-/out}"
mkdir -p "$OUT"

NPROC=$(nproc 2>/dev/null || echo 4)

# Shared emcc link flags (can be overridden via EMCC_FLAGS env)
LINK_FLAGS="${EMCC_FLAGS:--O3 -flto -msimd128 --no-entry \
  -sSTANDALONE_WASM=1 -sFILESYSTEM=0 -sASSERTIONS=0 \
  -sALLOW_MEMORY_GROWTH=1 -sMALLOC=emmalloc -sINITIAL_MEMORY=16777216}"

echo "=== Building WASM decoders (${NPROC} cores) ==="
echo "Output: $OUT"
echo ""

# ──────────────────────────────────────────────
# HEVC — libde265 (autotools)
# ──────────────────────────────────────────────
build_hevc() {
  echo "--- HEVC (libde265) ---"
  cd /build

  if [ ! -d libde265 ]; then
    git clone --depth 1 --branch v1.0.15 https://github.com/strukturag/libde265.git
  fi

  cd libde265
  if [ ! -f libde265/.libs/libde265.a ]; then
    autoreconf -fi
    emconfigure ./configure \
      --disable-sse --disable-dec265 --disable-sherlock265 \
      --disable-shared --enable-static
    emmake make -j"$NPROC"
  fi
  cd /build

  echo "Linking hevc-simd.wasm..."
  # shellcheck disable=SC2086
  emcc /build/hevc/wrapper.c \
    -I/build/libde265 \
    -L/build/libde265/libde265/.libs -lde265 \
    $LINK_FLAGS \
    -sEXPORTED_FUNCTIONS='["_malloc","_free","_free_frame","_hevc_create_decoder","_hevc_configure","_hevc_decode","_hevc_flush","_hevc_destroy"]' \
    -o "$OUT/hevc-simd.wasm"

  echo "HEVC done: $(wc -c < "$OUT/hevc-simd.wasm") bytes"
}

# ──────────────────────────────────────────────
# AV1 — dav1d (Meson)
# ──────────────────────────────────────────────
build_av1() {
  echo "--- AV1 (dav1d) ---"
  cd /build

  if [ ! -d dav1d ]; then
    git clone --depth 1 --branch 1.5.0 https://code.videolan.org/videolan/dav1d.git
  fi

  if [ ! -f dav1d-build/src/libdav1d.a ]; then
    meson setup dav1d-build dav1d \
      --cross-file /build/wasm-cross.txt \
      --default-library=static \
      --buildtype=release \
      -Dbitdepths='["8"]' \
      -Denable_asm=false \
      -Denable_tools=false \
      -Denable_tests=false \
      -Dlogging=false
    ninja -C dav1d-build -j"$NPROC"
  fi

  echo "Linking av1-simd.wasm..."
  # shellcheck disable=SC2086
  emcc /build/av1/wrapper.c \
    -I/build/dav1d/include \
    -I/build/dav1d-build/include \
    -L/build/dav1d-build/src -ldav1d \
    $LINK_FLAGS \
    -sEXPORTED_FUNCTIONS='["_malloc","_free","_free_frame","_av1_create_decoder","_av1_configure","_av1_decode","_av1_flush","_av1_destroy"]' \
    -o "$OUT/av1-simd.wasm"

  echo "AV1 done: $(wc -c < "$OUT/av1-simd.wasm") bytes"
}

# ──────────────────────────────────────────────
# VP9 — libvpx (autotools-style configure)
# ──────────────────────────────────────────────
build_vp9() {
  echo "--- VP9 (libvpx) ---"
  cd /build

  if [ ! -d libvpx ]; then
    git clone --depth 1 --branch v1.14.1 https://chromium.googlesource.com/webm/libvpx
  fi

  cd libvpx
  if [ ! -f libvpx.a ]; then
    emconfigure ./configure \
      --target=generic-gnu \
      --disable-examples --disable-tools --disable-docs \
      --disable-unit-tests --disable-vp8 \
      --enable-vp9-decoder --disable-vp9-encoder \
      --disable-multithread --disable-runtime-cpu-detect \
      --enable-static --disable-shared
    emmake make -j"$NPROC"
  fi
  cd /build

  echo "Linking vp9-simd.wasm..."
  # shellcheck disable=SC2086
  emcc /build/vp9/wrapper.c \
    -I/build/libvpx \
    -L/build/libvpx -lvpx \
    $LINK_FLAGS \
    -sEXPORTED_FUNCTIONS='["_malloc","_free","_free_frame","_vp9_create_decoder","_vp9_configure","_vp9_decode","_vp9_flush","_vp9_destroy"]' \
    -o "$OUT/vp9-simd.wasm"

  echo "VP9 done: $(wc -c < "$OUT/vp9-simd.wasm") bytes"
}

# ──────────────────────────────────────────────
# Build all codecs
# ──────────────────────────────────────────────
build_hevc
build_av1
build_vp9

echo ""
echo "=== All WASM decoders built ==="
ls -lh "$OUT"/*.wasm
