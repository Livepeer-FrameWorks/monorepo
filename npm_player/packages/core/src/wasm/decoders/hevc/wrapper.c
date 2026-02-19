/*
 * HEVC WASM Decoder Wrapper
 *
 * Thin wrapper around libde265 that exposes our standard ABI:
 *   hevc_create_decoder, hevc_configure, hevc_decode, hevc_flush,
 *   hevc_destroy, free_frame, malloc, free
 *
 * Compiled with Emscripten: emcc wrapper.c -lde265 -O3 -msimd128 ...
 * See ../build.sh for full build instructions.
 */

#include <stdlib.h>
#include <string.h>
#include <libde265/de265.h>

#include <emscripten/emscripten.h>

/* Output frame struct — must match WasmDecoderLoader.ts readYUVFrame() layout */
typedef struct {
  int32_t width;        /* offset 0 */
  int32_t height;       /* offset 4 */
  int32_t chromaFormat;  /* offset 8:  420, 422, 444 */
  int32_t bitDepth;     /* offset 12: 8 or 10 */
  int32_t yPtr;         /* offset 16 */
  int32_t uPtr;         /* offset 20 */
  int32_t vPtr;         /* offset 24 */
  int32_t ySize;        /* offset 28 */
  int32_t uvSize;       /* offset 32 */
} DecodedFrame;

typedef struct {
  de265_decoder_context *ctx;
} HevcDecoder;

static int chroma_to_format(enum de265_chroma c) {
  switch (c) {
    case de265_chroma_444: return 444;
    case de265_chroma_422: return 422;
    default:               return 420;
  }
}

/* Extract a decoded picture into our DecodedFrame struct.
 * Copies plane data so libde265 can reuse its internal buffers. */
static DecodedFrame *extract_picture(const struct de265_image *img) {
  if (!img) return NULL;

  int width  = de265_get_image_width(img, 0);
  int height = de265_get_image_height(img, 0);
  int bpp    = de265_get_bits_per_pixel(img, 0);
  enum de265_chroma chroma = de265_get_chroma_format(img);

  int yStride, uStride, vStride;
  const uint8_t *yPlane = de265_get_image_plane(img, 0, &yStride);
  const uint8_t *uPlane = de265_get_image_plane(img, 1, &uStride);
  const uint8_t *vPlane = de265_get_image_plane(img, 2, &vStride);

  if (!yPlane || !uPlane || !vPlane) return NULL;

  /* Chroma dimensions depend on subsampling */
  int chromaW = width, chromaH = height;
  if (chroma == de265_chroma_420) {
    chromaW = (width + 1) / 2;
    chromaH = (height + 1) / 2;
  } else if (chroma == de265_chroma_422) {
    chromaW = (width + 1) / 2;
  }

  int bytesPerSample = (bpp > 8) ? 2 : 1;
  int ySize  = width * height * bytesPerSample;
  int uvSize = chromaW * chromaH * bytesPerSample;

  /* Allocate output buffers */
  uint8_t *yOut = (uint8_t *)malloc(ySize);
  uint8_t *uOut = (uint8_t *)malloc(uvSize);
  uint8_t *vOut = (uint8_t *)malloc(uvSize);
  DecodedFrame *frame = (DecodedFrame *)malloc(sizeof(DecodedFrame));

  if (!yOut || !uOut || !vOut || !frame) {
    free(yOut); free(uOut); free(vOut); free(frame);
    return NULL;
  }

  /* Copy Y plane (may have stride padding) */
  int yRowBytes = width * bytesPerSample;
  for (int row = 0; row < height; row++) {
    memcpy(yOut + row * yRowBytes, yPlane + row * yStride, yRowBytes);
  }

  /* Copy U/V planes */
  int uvRowBytes = chromaW * bytesPerSample;
  for (int row = 0; row < chromaH; row++) {
    memcpy(uOut + row * uvRowBytes, uPlane + row * uStride, uvRowBytes);
    memcpy(vOut + row * uvRowBytes, vPlane + row * vStride, uvRowBytes);
  }

  frame->width       = width;
  frame->height      = height;
  frame->chromaFormat = chroma_to_format(chroma);
  frame->bitDepth    = bpp;
  frame->yPtr        = (int32_t)(uintptr_t)yOut;
  frame->uPtr        = (int32_t)(uintptr_t)uOut;
  frame->vPtr        = (int32_t)(uintptr_t)vOut;
  frame->ySize       = ySize;
  frame->uvSize      = uvSize;

  return frame;
}

EMSCRIPTEN_KEEPALIVE
int32_t hevc_create_decoder(void) {
  HevcDecoder *dec = (HevcDecoder *)malloc(sizeof(HevcDecoder));
  if (!dec) return 0;

  dec->ctx = de265_new_decoder();
  if (!dec->ctx) { free(dec); return 0; }

  /* Single-threaded in WASM (no pthreads) */
  de265_start_worker_threads(dec->ctx, 0);

  return (int32_t)(uintptr_t)dec;
}

EMSCRIPTEN_KEEPALIVE
void hevc_configure(int32_t handle, const uint8_t *config, int32_t size) {
  HevcDecoder *dec = (HevcDecoder *)(uintptr_t)handle;
  if (!dec || !dec->ctx || !config || size <= 0) return;

  /* Push SPS/PPS NAL units as configuration data */
  de265_push_NAL(dec->ctx, config, size, 0, NULL);

  /* Decode to process the parameter sets */
  int more = 0;
  de265_decode(dec->ctx, &more);
}

EMSCRIPTEN_KEEPALIVE
int32_t hevc_decode(int32_t handle, const uint8_t *data, int32_t size,
                    int32_t is_keyframe) {
  (void)is_keyframe; /* libde265 detects keyframes internally */
  HevcDecoder *dec = (HevcDecoder *)(uintptr_t)handle;
  if (!dec || !dec->ctx || !data || size <= 0) return 0;

  de265_push_NAL(dec->ctx, data, size, 0, NULL);

  int more = 0;
  de265_decode(dec->ctx, &more);

  const struct de265_image *img = de265_get_next_picture(dec->ctx);
  if (!img) return 0;

  DecodedFrame *frame = extract_picture(img);
  de265_release_next_picture(dec->ctx);

  return (int32_t)(uintptr_t)frame;
}

EMSCRIPTEN_KEEPALIVE
int32_t hevc_flush(int32_t handle) {
  HevcDecoder *dec = (HevcDecoder *)(uintptr_t)handle;
  if (!dec || !dec->ctx) return 0;

  de265_flush_data(dec->ctx);

  int more = 0;
  de265_decode(dec->ctx, &more);

  const struct de265_image *img = de265_get_next_picture(dec->ctx);
  if (!img) return 0;

  DecodedFrame *frame = extract_picture(img);
  de265_release_next_picture(dec->ctx);

  return (int32_t)(uintptr_t)frame;
}

EMSCRIPTEN_KEEPALIVE
void hevc_destroy(int32_t handle) {
  HevcDecoder *dec = (HevcDecoder *)(uintptr_t)handle;
  if (!dec) return;

  if (dec->ctx) {
    de265_free_decoder(dec->ctx);
  }
  free(dec);
}

EMSCRIPTEN_KEEPALIVE
void free_frame(int32_t ptr) {
  DecodedFrame *frame = (DecodedFrame *)(uintptr_t)ptr;
  if (!frame) return;

  free((void *)(uintptr_t)frame->yPtr);
  free((void *)(uintptr_t)frame->uPtr);
  free((void *)(uintptr_t)frame->vPtr);
  free(frame);
}
