/**
 * TextOverlaySource
 *
 * Renders text to an OffscreenCanvas → ImageBitmap for use as a compositor source.
 * Text rendering must happen on the main thread (workers lack font access).
 *
 * The bitmap is as large as the text plus its padding, in output pixels, so a
 * lower third covers only its own band. The constructor sets the largest size
 * a bitmap may reach.
 */

export interface TextOverlayConfig {
  /** Lines are separated by a newline. */
  text: string;
  fontFamily?: string;
  /** In output pixels: 48 is a lower third on a 1080p output. */
  fontSize?: number;
  fontWeight?: string;
  color?: string;
  backgroundColor?: string;
  /** Defaults to half the font size. */
  padding?: number;
  textAlign?: "left" | "center" | "right";
  borderRadius?: number;
  style?: "plain" | "lower-third" | "banner";
}

const DEFAULTS = {
  fontFamily: "sans-serif",
  fontSize: 48,
  fontWeight: "bold",
  color: "#FFFFFF",
  backgroundColor: "transparent",
  textAlign: "center",
  borderRadius: 0,
  style: "plain",
} satisfies Omit<TextOverlayConfig, "text">;

/** What overlay controls start from and how far they go. */
export const TEXT_OVERLAY_DEFAULTS = {
  fontSize: DEFAULTS.fontSize,
  minFontSize: 16,
  maxFontSize: 160,
  color: "#ffffff",
} as const;

const LINE_HEIGHT = 1.25;

export class TextOverlaySource {
  private canvas: OffscreenCanvas;
  private ctx: OffscreenCanvasRenderingContext2D;
  private _sourceId: string;
  private maxWidth: number;
  private maxHeight: number;
  private lastConfig: TextOverlayConfig | null = null;

  constructor(sourceId: string, maxWidth: number, maxHeight: number) {
    this._sourceId = sourceId;
    this.maxWidth = maxWidth;
    this.maxHeight = maxHeight;
    this.canvas = new OffscreenCanvas(maxWidth, maxHeight);
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

    if (opts.style === "lower-third") {
      opts.backgroundColor =
        opts.backgroundColor === "transparent" ? "rgba(0, 0, 0, 0.7)" : opts.backgroundColor;
      opts.textAlign = config.textAlign ?? "left";
    } else if (opts.style === "banner") {
      opts.backgroundColor =
        opts.backgroundColor === "transparent" ? "rgba(0, 0, 0, 0.85)" : opts.backgroundColor;
      opts.textAlign = config.textAlign ?? "center";
    }

    const lines = opts.text.split("\n");
    const font = `${opts.fontWeight} ${opts.fontSize}px ${opts.fontFamily}`;

    // An emptied text field leaves a blank pixel: a canvas without an area has
    // no bitmap, and the overlay comes back with the next character typed.
    if (!opts.text.trim()) {
      this.canvas.width = 1;
      this.canvas.height = 1;
      return this.canvas.transferToImageBitmap();
    }

    const padding = config.padding ?? Math.round(opts.fontSize / 2);
    const lineHeight = Math.ceil(opts.fontSize * LINE_HEIGHT);

    ctx.font = font;
    const textWidth = Math.max(...lines.map((line) => ctx.measureText(line).width));
    const w = Math.min(Math.ceil(textWidth) + padding * 2, this.maxWidth);
    const h = Math.min(lineHeight * lines.length + padding * 2, this.maxHeight);

    // Resizing a canvas clears it and resets its context, the font included.
    this.canvas.width = w;
    this.canvas.height = h;

    if (opts.backgroundColor !== "transparent") {
      ctx.fillStyle = opts.backgroundColor;
      if (opts.borderRadius > 0) {
        this.roundRect(ctx, 0, 0, w, h, opts.borderRadius);
        ctx.fill();
      } else {
        ctx.fillRect(0, 0, w, h);
      }
    }

    ctx.fillStyle = opts.color;
    ctx.font = font;
    ctx.textBaseline = "middle";
    ctx.textAlign = opts.textAlign;

    let x: number;
    switch (opts.textAlign) {
      case "left":
        x = padding;
        break;
      case "right":
        x = w - padding;
        break;
      default:
        x = w / 2;
    }

    lines.forEach((line, index) => {
      ctx.fillText(line, x, padding + lineHeight * (index + 0.5), w - padding * 2);
    });

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
