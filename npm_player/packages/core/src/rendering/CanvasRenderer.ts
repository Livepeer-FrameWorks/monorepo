/**
 * Canvas2D Fallback Renderer
 *
 * Simple fallback for environments without WebGL support.
 * Uses Canvas2D drawImage for VideoFrames and manual YUV→RGB for raw planes.
 */

import type { YUVPlanes, ColorPrimaries, TransferFunction, ColorRange } from "./WebGLRenderer";

const COLOR_SPACE_COEFFICIENTS: Record<string, [number, number]> = {
  bt601: [0.299, 0.114],
  bt709: [0.2126, 0.0722],
  bt2020: [0.2627, 0.0593],
};

export class CanvasRenderer {
  private canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D;
  private currentWidth = 0;
  private currentHeight = 0;
  private destroyed = false;
  private primaries: ColorPrimaries = "bt709";
  private range: ColorRange = "limited";

  // Reusable buffer for YUV→RGB conversion
  private rgbBuffer: ImageData | null = null;

  constructor(canvas: HTMLCanvasElement) {
    this.canvas = canvas;
    const ctx = canvas.getContext("2d", { desynchronized: true, alpha: false });
    if (!ctx) throw new Error("Canvas 2D context not supported");
    this.ctx = ctx;
  }

  /**
   * Render a VideoFrame via Canvas2D drawImage.
   */
  render(frame: VideoFrame): void {
    if (this.destroyed) return;

    this.resize(frame.displayWidth, frame.displayHeight);
    this.ctx.drawImage(
      frame as unknown as CanvasImageSource,
      0,
      0,
      this.canvas.width,
      this.canvas.height
    );
    frame.close();
  }

  /**
   * Render raw YUV planes via CPU conversion + putImageData.
   */
  renderYUV(planes: YUVPlanes): void {
    if (this.destroyed) return;

    const { width, height, y, u, v, format } = planes;
    this.resize(width, height);

    if (!this.rgbBuffer || this.rgbBuffer.width !== width || this.rgbBuffer.height !== height) {
      this.rgbBuffer = this.ctx.createImageData(width, height);
    }

    const [kr, kb] = COLOR_SPACE_COEFFICIENTS[this.primaries] ?? COLOR_SPACE_COEFFICIENTS["bt709"];
    const kg = 1 - kr - kb;
    const rgb = this.rgbBuffer.data;
    const limited = this.range === "limited";
    const chromaW = format === "I444" ? width : width >> 1;

    for (let row = 0; row < height; row++) {
      const chromaRow = format === "I422" || format === "I444" ? row : row >> 1;
      for (let col = 0; col < width; col++) {
        const chromaCol = format === "I444" ? col : col >> 1;

        let yVal = y[row * width + col] as number;
        let uVal = u[chromaRow * chromaW + chromaCol] as number;
        let vVal = v[chromaRow * chromaW + chromaCol] as number;

        // Normalize to 0-1
        const maxVal = format === "I420P10" ? 1023 : 255;
        yVal /= maxVal;
        uVal /= maxVal;
        vVal /= maxVal;

        // Range adjustment
        if (limited) {
          yVal = (yVal - 16 / 255) * (255 / (235 - 16));
          uVal = (uVal - 16 / 255) * (255 / (240 - 16));
          vVal = (vVal - 16 / 255) * (255 / (240 - 16));
        }

        // Center chroma
        uVal -= 0.5;
        vVal -= 0.5;

        // YUV→RGB
        const r = yVal + (2 - 2 * kr) * vVal;
        const g = yVal - ((2 * kb * (1 - kb)) / kg) * uVal - ((2 * kr * (1 - kr)) / kg) * vVal;
        const b = yVal + (2 - 2 * kb) * uVal;

        const idx = (row * width + col) * 4;
        rgb[idx] = Math.max(0, Math.min(255, r * 255)) | 0;
        rgb[idx + 1] = Math.max(0, Math.min(255, g * 255)) | 0;
        rgb[idx + 2] = Math.max(0, Math.min(255, b * 255)) | 0;
        rgb[idx + 3] = 255;
      }
    }

    this.ctx.putImageData(this.rgbBuffer, 0, 0);
  }

  setColorSpace(primaries: ColorPrimaries, _transfer: TransferFunction, range?: ColorRange): void {
    this.primaries = primaries;
    if (range) this.range = range;
  }

  resize(width: number, height: number): void {
    if (width === this.currentWidth && height === this.currentHeight) return;
    this.currentWidth = width;
    this.currentHeight = height;
    this.canvas.width = width;
    this.canvas.height = height;
  }

  snapshot(type: "png" | "jpeg" | "webp" = "png", quality = 0.92): string {
    return this.canvas.toDataURL(`image/${type}`, quality);
  }

  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    this.rgbBuffer = null;
  }
}
