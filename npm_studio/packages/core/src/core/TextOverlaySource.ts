/**
 * TextOverlaySource
 *
 * Renders text to an OffscreenCanvas → ImageBitmap for use as a compositor source.
 * Text rendering must happen on the main thread (workers lack font access).
 */

export interface TextOverlayConfig {
  text: string;
  fontFamily?: string;
  fontSize?: number;
  fontWeight?: string;
  color?: string;
  backgroundColor?: string;
  padding?: number;
  textAlign?: "left" | "center" | "right";
  borderRadius?: number;
  style?: "plain" | "lower-third" | "banner";
}

const DEFAULTS: Required<Omit<TextOverlayConfig, "text">> = {
  fontFamily: "sans-serif",
  fontSize: 48,
  fontWeight: "bold",
  color: "#FFFFFF",
  backgroundColor: "transparent",
  padding: 16,
  textAlign: "center",
  borderRadius: 0,
  style: "plain",
};

export class TextOverlaySource {
  private canvas: OffscreenCanvas;
  private ctx: OffscreenCanvasRenderingContext2D;
  private _sourceId: string;
  private lastConfig: TextOverlayConfig | null = null;

  constructor(sourceId: string, width: number, height: number) {
    this._sourceId = sourceId;
    this.canvas = new OffscreenCanvas(width, height);
    const ctx = this.canvas.getContext("2d");
    if (!ctx) throw new Error("Failed to create 2D context for TextOverlaySource");
    this.ctx = ctx;
  }

  get sourceId(): string {
    return this._sourceId;
  }

  render(config: TextOverlayConfig): ImageBitmap {
    this.lastConfig = config;
    const opts = { ...DEFAULTS, ...config };
    const ctx = this.ctx;
    const w = this.canvas.width;
    const h = this.canvas.height;

    ctx.clearRect(0, 0, w, h);

    // Apply style presets
    if (opts.style === "lower-third") {
      opts.backgroundColor =
        opts.backgroundColor === "transparent" ? "rgba(0, 0, 0, 0.7)" : opts.backgroundColor;
      opts.textAlign = "left";
      opts.padding = opts.padding || 24;
    } else if (opts.style === "banner") {
      opts.backgroundColor =
        opts.backgroundColor === "transparent" ? "rgba(0, 0, 0, 0.85)" : opts.backgroundColor;
      opts.textAlign = "center";
      opts.padding = opts.padding || 32;
    }

    // Draw background
    if (opts.backgroundColor !== "transparent") {
      ctx.fillStyle = opts.backgroundColor;
      if (opts.borderRadius > 0) {
        this.roundRect(ctx, 0, 0, w, h, opts.borderRadius);
        ctx.fill();
      } else {
        ctx.fillRect(0, 0, w, h);
      }
    }

    // Draw text
    ctx.fillStyle = opts.color;
    ctx.font = `${opts.fontWeight} ${opts.fontSize}px ${opts.fontFamily}`;
    ctx.textBaseline = "middle";
    ctx.textAlign = opts.textAlign;

    let x: number;
    switch (opts.textAlign) {
      case "left":
        x = opts.padding;
        break;
      case "right":
        x = w - opts.padding;
        break;
      default:
        x = w / 2;
    }

    ctx.fillText(opts.text, x, h / 2, w - opts.padding * 2);

    return this.canvas.transferToImageBitmap();
  }

  getLastConfig(): TextOverlayConfig | null {
    return this.lastConfig;
  }

  private roundRect(
    ctx: OffscreenCanvasRenderingContext2D,
    x: number,
    y: number,
    w: number,
    h: number,
    r: number
  ): void {
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.lineTo(x + w - r, y);
    ctx.quadraticCurveTo(x + w, y, x + w, y + r);
    ctx.lineTo(x + w, y + h - r);
    ctx.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
    ctx.lineTo(x + r, y + h);
    ctx.quadraticCurveTo(x, y + h, x, y + h - r);
    ctx.lineTo(x, y + r);
    ctx.quadraticCurveTo(x, y, x + r, y);
    ctx.closePath();
  }
}
