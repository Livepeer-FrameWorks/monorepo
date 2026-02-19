/*
 * VP9 WASM Decoder Wrapper
 *
 * Thin wrapper around libvpx that exposes our standard ABI:
 *   vp9_create_decoder, vp9_configure, vp9_decode, vp9_flush,
 *   vp9_destroy, free_frame, malloc, free
 *
 * Compiled with Emscripten: emcc wrapper.c -lvpx -O3 -msimd128 ...
 * See ../build.sh for full build instructions.
 */

#include <stdlib.h>
#include <string.h>
#include <vpx/vpx_decoder.h>
#include <vpx/vp8dx.h>

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
  vpx_codec_ctx_t codec;
  int initialized;
} Vp9Decoder;

static int vpx_fmt_to_chroma(vpx_img_fmt_t fmt) {
  switch (fmt) {
    case VPX_IMG_FMT_I444:
    case VPX_IMG_FMT_I44416:
      return 444;
    case VPX_IMG_FMT_I422:
    case VPX_IMG_FMT_I42216:
      return 422;
    default:
      return 420;
  }
}

static int vpx_fmt_bpc(vpx_img_fmt_t fmt) {
  /* High-bitdepth formats have the highbitdepth flag set */
  return (fmt & VPX_IMG_FMT_HIGHBITDEPTH) ? 10 : 8;
}

/* Extract a decoded frame into our DecodedFrame struct */
static DecodedFrame *extract_image(vpx_image_t *img) {
  if (!img) return NULL;

  int width  = img->d_w;
  int height = img->d_h;
  int bpc    = vpx_fmt_bpc(img->fmt);
  int chromaFmt = vpx_fmt_to_chroma(img->fmt);

  int chromaW = width, chromaH = height;
  if (chromaFmt == 420) {
    chromaW = (width + 1) / 2;
    chromaH = (height + 1) / 2;
  } else if (chromaFmt == 422) {
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

  /* Copy Y plane */
  int yRowBytes = width * bytesPerSample;
  for (unsigned row = 0; row < (unsigned)height; row++) {
    memcpy(yOut + row * yRowBytes,
           img->planes[VPX_PLANE_Y] + row * img->stride[VPX_PLANE_Y],
           yRowBytes);
  }

  /* Copy U/V planes */
  int uvRowBytes = chromaW * bytesPerSample;
  for (unsigned row = 0; row < (unsigned)chromaH; row++) {
    memcpy(uOut + row * uvRowBytes,
           img->planes[VPX_PLANE_U] + row * img->stride[VPX_PLANE_U],
           uvRowBytes);
    memcpy(vOut + row * uvRowBytes,
           img->planes[VPX_PLANE_V] + row * img->stride[VPX_PLANE_V],
           uvRowBytes);
  }

  frame->width       = width;
  frame->height      = height;
  frame->chromaFormat = chromaFmt;
  frame->bitDepth    = bpc;
  frame->yPtr        = (int32_t)(uintptr_t)yOut;
  frame->uPtr        = (int32_t)(uintptr_t)uOut;
  frame->vPtr        = (int32_t)(uintptr_t)vOut;
  frame->ySize       = ySize;
  frame->uvSize      = uvSize;

  return frame;
}

EMSCRIPTEN_KEEPALIVE
int32_t vp9_create_decoder(void) {
  Vp9Decoder *dec = (Vp9Decoder *)malloc(sizeof(Vp9Decoder));
  if (!dec) return 0;

  memset(dec, 0, sizeof(Vp9Decoder));

  vpx_codec_dec_cfg_t cfg;
  memset(&cfg, 0, sizeof(cfg));
  cfg.threads = 1;

  vpx_codec_err_t ret = vpx_codec_dec_init(
    &dec->codec, vpx_codec_vp9_dx(), &cfg, 0);
  if (ret != VPX_CODEC_OK) {
    free(dec);
    return 0;
  }

  dec->initialized = 1;
  return (int32_t)(uintptr_t)dec;
}

EMSCRIPTEN_KEEPALIVE
void vp9_configure(int32_t handle, const uint8_t *config, int32_t size) {
  /* VP9 doesn't need external configuration — codec config is inline */
  (void)handle;
  (void)config;
  (void)size;
}

EMSCRIPTEN_KEEPALIVE
int32_t vp9_decode(int32_t handle, const uint8_t *data, int32_t size,
                   int32_t is_keyframe) {
  (void)is_keyframe;
  Vp9Decoder *dec = (Vp9Decoder *)(uintptr_t)handle;
  if (!dec || !dec->initialized || !data || size <= 0) return 0;

  vpx_codec_err_t ret = vpx_codec_decode(
    &dec->codec, data, (unsigned int)size, NULL, 0);
  if (ret != VPX_CODEC_OK) return 0;

  vpx_codec_iter_t iter = NULL;
  vpx_image_t *img = vpx_codec_get_frame(&dec->codec, &iter);
  if (!img) return 0;

  return (int32_t)(uintptr_t)extract_image(img);
}

EMSCRIPTEN_KEEPALIVE
int32_t vp9_flush(int32_t handle) {
  Vp9Decoder *dec = (Vp9Decoder *)(uintptr_t)handle;
  if (!dec || !dec->initialized) return 0;

  /* Flush by decoding NULL data */
  vpx_codec_decode(&dec->codec, NULL, 0, NULL, 0);

  vpx_codec_iter_t iter = NULL;
  vpx_image_t *img = vpx_codec_get_frame(&dec->codec, &iter);
  if (!img) return 0;

  return (int32_t)(uintptr_t)extract_image(img);
}

EMSCRIPTEN_KEEPALIVE
void vp9_destroy(int32_t handle) {
  Vp9Decoder *dec = (Vp9Decoder *)(uintptr_t)handle;
  if (!dec) return;

  if (dec->initialized) {
    vpx_codec_destroy(&dec->codec);
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
