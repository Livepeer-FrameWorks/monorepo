/*
 * AV1 WASM Decoder Wrapper
 *
 * Thin wrapper around dav1d that exposes our standard ABI:
 *   av1_create_decoder, av1_configure, av1_decode, av1_flush,
 *   av1_destroy, free_frame, malloc, free
 *
 * Compiled with Emscripten: emcc wrapper.c -ldav1d -O3 -msimd128 ...
 * See ../build.sh for full build instructions.
 */

#include <stdlib.h>
#include <string.h>
#include <dav1d/dav1d.h>

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
  Dav1dContext *ctx;
  Dav1dSettings settings;
} Av1Decoder;

static int layout_to_format(enum Dav1dPixelLayout layout) {
  switch (layout) {
    case DAV1D_PIXEL_LAYOUT_I444: return 444;
    case DAV1D_PIXEL_LAYOUT_I422: return 422;
    default:                       return 420;
  }
}

/* Extract a decoded picture into our DecodedFrame struct.
 * Copies plane data so dav1d can reuse its internal buffers. */
static DecodedFrame *extract_picture(Dav1dPicture *pic) {
  if (!pic || !pic->data[0]) return NULL;

  int width  = pic->p.w;
  int height = pic->p.h;
  int bpc    = pic->p.bpc;
  enum Dav1dPixelLayout layout = pic->p.layout;

  int chromaW = width, chromaH = height;
  if (layout == DAV1D_PIXEL_LAYOUT_I420) {
    chromaW = (width + 1) / 2;
    chromaH = (height + 1) / 2;
  } else if (layout == DAV1D_PIXEL_LAYOUT_I422) {
    chromaW = (width + 1) / 2;
  }

  int bytesPerSample = (bpc > 8) ? 2 : 1;
  int ySize  = width * height * bytesPerSample;
  int uvSize = chromaW * chromaH * bytesPerSample;

  uint8_t *yOut = (uint8_t *)malloc(ySize);
  uint8_t *uOut = (uint8_t *)malloc(uvSize);
  uint8_t *vOut = (uint8_t *)malloc(uvSize);
  DecodedFrame *frame = (DecodedFrame *)malloc(sizeof(DecodedFrame));

  if (!yOut || !uOut || !vOut || !frame) {
    free(yOut); free(uOut); free(vOut); free(frame);
    return NULL;
  }

  /* Copy Y plane (stride may differ from width) */
  ptrdiff_t yStride = pic->stride[0];
  int yRowBytes = width * bytesPerSample;
  const uint8_t *ySrc = (const uint8_t *)pic->data[0];
  for (int row = 0; row < height; row++) {
    memcpy(yOut + row * yRowBytes, ySrc + row * yStride, yRowBytes);
  }

  /* Copy U/V planes */
  ptrdiff_t uvStride = pic->stride[1];
  int uvRowBytes = chromaW * bytesPerSample;
  const uint8_t *uSrc = (const uint8_t *)pic->data[1];
  const uint8_t *vSrc = (const uint8_t *)pic->data[2];
  for (int row = 0; row < chromaH; row++) {
    memcpy(uOut + row * uvRowBytes, uSrc + row * uvStride, uvRowBytes);
    memcpy(vOut + row * uvRowBytes, vSrc + row * uvStride, uvRowBytes);
  }

  frame->width       = width;
  frame->height      = height;
  frame->chromaFormat = layout_to_format(layout);
  frame->bitDepth    = bpc;
  frame->yPtr        = (int32_t)(uintptr_t)yOut;
  frame->uPtr        = (int32_t)(uintptr_t)uOut;
  frame->vPtr        = (int32_t)(uintptr_t)vOut;
  frame->ySize       = ySize;
  frame->uvSize      = uvSize;

  return frame;
}

EMSCRIPTEN_KEEPALIVE
int32_t av1_create_decoder(void) {
  Av1Decoder *dec = (Av1Decoder *)malloc(sizeof(Av1Decoder));
  if (!dec) return 0;

  dav1d_default_settings(&dec->settings);
  dec->settings.n_threads = 1; /* Single-threaded in WASM */
  dec->settings.max_frame_delay = 1;

  int ret = dav1d_open(&dec->ctx, &dec->settings);
  if (ret < 0) { free(dec); return 0; }

  return (int32_t)(uintptr_t)dec;
}

EMSCRIPTEN_KEEPALIVE
void av1_configure(int32_t handle, const uint8_t *config, int32_t size) {
  /* dav1d doesn't need external configuration — sequence headers are
   * parsed inline from the OBU stream. This is a no-op. */
  (void)handle;
  (void)config;
  (void)size;
}

EMSCRIPTEN_KEEPALIVE
int32_t av1_decode(int32_t handle, const uint8_t *data, int32_t size,
                   int32_t is_keyframe) {
  (void)is_keyframe;
  Av1Decoder *dec = (Av1Decoder *)(uintptr_t)handle;
  if (!dec || !dec->ctx || !data || size <= 0) return 0;

  /* Wrap input data for dav1d */
  Dav1dData dav1d_data;
  memset(&dav1d_data, 0, sizeof(dav1d_data));
  uint8_t *buf = dav1d_data_create(&dav1d_data, size);
  if (!buf) return 0;
  memcpy(buf, data, size);

  /* Feed data to decoder — may need multiple calls if EAGAIN */
  int ret;
  do {
    ret = dav1d_send_data(dec->ctx, &dav1d_data);
  } while (ret == DAV1D_ERR(EAGAIN));

  /* Try to get a decoded picture */
  Dav1dPicture pic;
  memset(&pic, 0, sizeof(pic));
  ret = dav1d_get_picture(dec->ctx, &pic);
  if (ret < 0) {
    /* No picture available yet (reordering) */
    return 0;
  }

  DecodedFrame *frame = extract_picture(&pic);
  dav1d_picture_unref(&pic);

  return (int32_t)(uintptr_t)frame;
}

EMSCRIPTEN_KEEPALIVE
int32_t av1_flush(int32_t handle) {
  Av1Decoder *dec = (Av1Decoder *)(uintptr_t)handle;
  if (!dec || !dec->ctx) return 0;

  dav1d_flush(dec->ctx);

  Dav1dPicture pic;
  memset(&pic, 0, sizeof(pic));
  int ret = dav1d_get_picture(dec->ctx, &pic);
  if (ret < 0) return 0;

  DecodedFrame *frame = extract_picture(&pic);
  dav1d_picture_unref(&pic);

  return (int32_t)(uintptr_t)frame;
}

EMSCRIPTEN_KEEPALIVE
void av1_destroy(int32_t handle) {
  Av1Decoder *dec = (Av1Decoder *)(uintptr_t)handle;
  if (!dec) return;

  if (dec->ctx) {
    dav1d_close(&dec->ctx);
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
