# WASM Decoder Build Environment
#
# Builds HEVC (libde265), AV1 (dav1d), and VP9 (libvpx) as standalone WASM modules.
#
# Usage:
#   docker build -t fw-wasm-decoders .
#   docker run --rm -v $(pwd)/prebuilt:/out fw-wasm-decoders
#
# Output: /out/hevc-simd.wasm, /out/av1-simd.wasm, /out/vp9-simd.wasm

FROM emscripten/emsdk:3.1.51

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3-pip ninja-build pkg-config autoconf automake libtool \
  && pip3 install meson \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Copy wrapper sources and build script
COPY hevc/ /build/hevc/
COPY av1/ /build/av1/
COPY vp9/ /build/vp9/
COPY wasm-cross.txt /build/
COPY build.sh /build/

RUN chmod +x /build/build.sh

# Common emcc flags shared across all codecs
ENV EMCC_FLAGS="-O3 -flto -msimd128 --no-entry \
  -sSTANDALONE_WASM=1 -sFILESYSTEM=0 -sASSERTIONS=0 \
  -sALLOW_MEMORY_GROWTH=1 -sMALLOC=emmalloc -sINITIAL_MEMORY=16777216"

ENTRYPOINT ["/build/build.sh"]
