import { parseThumbnailVtt, findCueAtTime, type ThumbnailCue } from "./ThumbnailVttParser";

export interface ThumbnailSpriteManagerOptions {
  vttUrl: string;
  baseUrl: string;
  isLive: boolean;
  refreshInterval?: number;
  onCuesChange?: (cues: ThumbnailCue[]) => void;
  log?: (msg: string) => void;
}

const DEFAULT_REFRESH_INTERVAL = 5000;

export class ThumbnailSpriteManager {
  private cues: ThumbnailCue[] = [];
  private destroyed = false;
  private fetching = false;
  private abortController: AbortController | null = null;
  private refreshTimer: ReturnType<typeof setInterval> | null = null;
  private options: ThumbnailSpriteManagerOptions;
  private spriteObjectUrl: string | null = null;

  constructor(options: ThumbnailSpriteManagerOptions) {
    this.options = options;
    if (options.isLive) {
      this.startPush();
    } else {
      this.poll();
    }
  }

  /**
   * Push mode (live): persistent connection to ?mode=push.
   * Server sends multipart/mixed with VTT + JPEG pairs per regen.
   * Falls back to polling on failure.
   */
  private async startPush(): Promise<void> {
    if (this.destroyed) return;
    this.abortController = new AbortController();
    const pushUrl =
      this.options.vttUrl + (this.options.vttUrl.includes("?") ? "&" : "?") + "mode=push";

    // Timeout: if no data arrives within 10s, fall back to polling
    const timeout = setTimeout(() => {
      this.options.log?.("[ThumbnailSpriteManager] Push timeout, falling back to poll");
      this.abortController?.abort();
    }, 10000);

    try {
      const response = await fetch(pushUrl, { signal: this.abortController.signal });
      if (!response.ok || !response.body) {
        clearTimeout(timeout);
        this.options.log?.(
          `[ThumbnailSpriteManager] Push failed: ${response.status}, falling back to poll`
        );
        this.startPolling();
        return;
      }

      const contentType = response.headers.get("content-type") || "";
      const boundaryMatch = contentType.match(/boundary=(.+)/);
      if (!boundaryMatch) {
        clearTimeout(timeout);
        this.options.log?.("[ThumbnailSpriteManager] No multipart boundary, falling back to poll");
        this.startPolling();
        return;
      }
      const boundary = boundaryMatch[1].trim();
      const boundaryMarker = "--" + boundary;

      const reader = response.body.getReader();
      let buffer: Uint8Array<ArrayBufferLike> = new Uint8Array(0);
      let receivedData = false;

      while (!this.destroyed) {
        const { done, value } = await reader.read();
        if (done) break;

        if (!receivedData) {
          receivedData = true;
          clearTimeout(timeout);
        }

        const newBuf = new Uint8Array(buffer.length + value.length);
        newBuf.set(buffer);
        newBuf.set(value, buffer.length);
        buffer = newBuf;

        const result = this.processMultipart(buffer, boundaryMarker);
        if (result) buffer = result;
      }
    } catch (e: unknown) {
      clearTimeout(timeout);
      if (e instanceof DOMException && e.name === "AbortError") {
        // If aborted by timeout, fall through to polling below
      } else {
        this.options.log?.(`[ThumbnailSpriteManager] Push error: ${e}`);
      }
    }

    if (!this.destroyed) {
      this.options.log?.("[ThumbnailSpriteManager] Push ended, falling back to poll");
      this.startPolling();
    }
  }

  /**
   * Parse multipart buffer for VTT + JPEG parts.
   * Returns remaining buffer after processing, or null if nothing was processed.
   */
  private processMultipart(
    buffer: Uint8Array<ArrayBufferLike>,
    boundaryMarker: string
  ): Uint8Array<ArrayBufferLike> | null {
    const encoder = new TextEncoder();
    const boundaryBytes = encoder.encode(boundaryMarker);

    const positions: number[] = [];
    for (let i = 0; i <= buffer.length - boundaryBytes.length; i++) {
      let match = true;
      for (let j = 0; j < boundaryBytes.length; j++) {
        if (buffer[i + j] !== boundaryBytes[j]) {
          match = false;
          break;
        }
      }
      if (match) positions.push(i);
    }
    if (positions.length < 2) return null;

    const decoder = new TextDecoder();
    let pendingVtt: string | null = null;
    let pendingJpeg: Uint8Array<ArrayBufferLike> | null = null;
    let lastProcessedBoundary = -1;

    for (let i = 0; i < positions.length - 1; i++) {
      const partStart = positions[i] + boundaryBytes.length;
      const partEnd = positions[i + 1];
      const partBytes = buffer.slice(partStart, partEnd);
      const partText = decoder.decode(partBytes);

      const headerEnd = partText.indexOf("\r\n\r\n");
      if (headerEnd === -1) continue;

      const headers = partText.slice(0, headerEnd).toLowerCase();
      const bodyOffset = headerEnd + 4;

      if (headers.includes("text/vtt")) {
        pendingVtt = partText.slice(bodyOffset).trim();
      } else if (headers.includes("image/jpeg")) {
        const clMatch = partText.slice(0, headerEnd).match(/content-length:\s*(\d+)/i);
        if (clMatch) {
          const jpegLen = parseInt(clMatch[1], 10);
          const headerBytes = encoder.encode(partText.slice(0, bodyOffset));
          pendingJpeg = partBytes.slice(headerBytes.length, headerBytes.length + jpegLen);
        }
      }

      if (pendingVtt && pendingJpeg) {
        const rawCues = parseThumbnailVtt(pendingVtt);
        if (rawCues.length > 0) {
          if (this.spriteObjectUrl) URL.revokeObjectURL(this.spriteObjectUrl);
          const jpegCopy = new Uint8Array(pendingJpeg);
          const blob = new Blob([jpegCopy], { type: "image/jpeg" });
          this.spriteObjectUrl = URL.createObjectURL(blob);

          this.cues = rawCues.map((cue) => ({
            ...cue,
            url: this.spriteObjectUrl || this.resolveUrl(cue.url),
          }));
          this.options.log?.(`[ThumbnailSpriteManager] Push: ${this.cues.length} cues`);
          this.options.onCuesChange?.(this.cues);
        }
        pendingVtt = null;
        pendingJpeg = null;
        lastProcessedBoundary = i + 1;
      }
    }

    if (lastProcessedBoundary >= 0) {
      return buffer.slice(positions[lastProcessedBoundary]);
    }
    return null;
  }

  /** Poll fallback (live) or one-shot (VOD) */
  private startPolling(): void {
    if (this.destroyed) return;
    this.poll();
    if (this.options.isLive) {
      const interval = this.options.refreshInterval ?? DEFAULT_REFRESH_INTERVAL;
      this.refreshTimer = setInterval(() => this.poll(), interval);
    }
  }

  private async poll(): Promise<void> {
    if (this.destroyed || this.fetching) return;
    this.fetching = true;
    try {
      const response = await fetch(this.options.vttUrl);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const text = await response.text();
      if (this.destroyed) return;
      const rawCues = parseThumbnailVtt(text);
      this.cues = rawCues.map((cue) => ({
        ...cue,
        url: this.resolveUrl(cue.url),
      }));
      this.options.log?.(`[ThumbnailSpriteManager] Poll: ${this.cues.length} cues`);
      this.options.onCuesChange?.(this.cues);
    } catch (e) {
      this.options.log?.(`[ThumbnailSpriteManager] Poll failed: ${e}`);
    } finally {
      this.fetching = false;
    }
  }

  private resolveUrl(url: string): string {
    if (url.startsWith("http://") || url.startsWith("https://") || url.startsWith("//")) {
      return url;
    }
    const base = this.options.baseUrl.replace(/\/$/, "");
    if (url.startsWith("/")) return base + url;
    return base + "/" + url;
  }

  getCues(): ThumbnailCue[] {
    return this.cues;
  }

  findCueAtTime(timeSeconds: number): ThumbnailCue | null {
    return findCueAtTime(this.cues, timeSeconds);
  }

  updateVttUrl(vttUrl: string): void {
    if (vttUrl === this.options.vttUrl) return;
    this.options.vttUrl = vttUrl;
    this.abortController?.abort();
    this.abortController = null;
    if (this.refreshTimer) {
      clearInterval(this.refreshTimer);
      this.refreshTimer = null;
    }
    if (this.options.isLive) {
      this.startPush();
    } else {
      this.poll();
    }
  }

  destroy(): void {
    this.destroyed = true;
    this.abortController?.abort();
    this.abortController = null;
    if (this.refreshTimer) {
      clearInterval(this.refreshTimer);
      this.refreshTimer = null;
    }
    if (this.spriteObjectUrl) {
      URL.revokeObjectURL(this.spriteObjectUrl);
      this.spriteObjectUrl = null;
    }
    this.cues = [];
  }
}
