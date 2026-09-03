import type { MetaTrackEvent } from "../types";

const DEFAULT_CUE_DURATION_MS = 4_000;
const MIN_CUE_DURATION_MS = 250;
const PLAYBACK_CLOCK_POLL_MS = 100;
const SEEK_BEFORE_CUE_TOLERANCE_MS = 250;

export interface SubtitleOverlayRendererOptions {
  /** Playback timeline clock in milliseconds. Keeps cue lifetime pause/seek aware. */
  getPlaybackTimeMs?: () => number;
}

export function subtitleCueDurationMs(event: MetaTrackEvent): number {
  const payload =
    event.data && typeof event.data === "object" ? (event.data as Record<string, unknown>) : null;
  const endValue = payload?.endTime ?? payload?.end;
  const durationValue = payload?.durationMs ?? event.durationMs;
  const end =
    endValue === null || endValue === undefined || endValue === "" ? NaN : Number(endValue);
  const cueDuration =
    durationValue === null || durationValue === undefined || durationValue === ""
      ? NaN
      : Number(durationValue);
  const duration = Number.isFinite(end)
    ? end - event.timestamp
    : Number.isFinite(cueDuration)
      ? cueDuration
      : DEFAULT_CUE_DURATION_MS;
  return Math.max(MIN_CUE_DURATION_MS, duration);
}

export class SubtitleOverlayRenderer {
  private overlay: HTMLDivElement | null = null;
  private clearTimer: ReturnType<typeof setTimeout> | null = null;
  private cueStartMs: number | null = null;
  private cueEndMs: number | null = null;

  constructor(
    private readonly container: HTMLElement,
    private readonly options: SubtitleOverlayRendererOptions = {}
  ) {}

  render(event: MetaTrackEvent): void {
    const payload = event.data;
    const cue =
      payload && typeof payload === "object" ? (payload as Record<string, unknown>) : null;
    const text =
      typeof payload === "string" ? payload : typeof cue?.text === "string" ? cue.text : "";
    if (!text) {
      this.clearCue();
      return;
    }

    if (!this.overlay) {
      const overlay = document.createElement("div");
      overlay.className = "fw-player-subtitle-overlay";
      Object.assign(overlay.style, {
        position: "absolute",
        left: "10%",
        right: "10%",
        bottom: "8%",
        zIndex: "5",
        color: "white",
        textAlign: "center",
        fontSize: "clamp(16px, 3vw, 28px)",
        lineHeight: "1.3",
        textShadow: "0 1px 3px black, 0 0 6px black",
        pointerEvents: "none",
        whiteSpace: "pre-line",
      });
      this.container.appendChild(overlay);
      this.overlay = overlay;
    }
    this.overlay.textContent = text;
    this.cancelClearTimer();
    const durationMs = subtitleCueDurationMs(event);
    if (this.options.getPlaybackTimeMs) {
      this.cueStartMs = event.timestamp;
      this.cueEndMs = event.timestamp + durationMs;
      this.schedulePlaybackClockCheck();
    } else {
      this.clearTimer = setTimeout(() => this.clearCue(), durationMs);
    }
  }

  destroy(): void {
    this.cancelClearTimer();
    this.cueStartMs = null;
    this.cueEndMs = null;
    this.overlay?.remove();
    this.overlay = null;
  }

  private schedulePlaybackClockCheck(): void {
    this.clearTimer = setTimeout(() => {
      this.clearTimer = null;
      const playbackTime = this.options.getPlaybackTimeMs?.();
      if (
        this.cueStartMs === null ||
        this.cueEndMs === null ||
        typeof playbackTime !== "number" ||
        !Number.isFinite(playbackTime)
      ) {
        this.clearCue();
        return;
      }
      if (
        playbackTime >= this.cueEndMs ||
        playbackTime < this.cueStartMs - SEEK_BEFORE_CUE_TOLERANCE_MS
      ) {
        this.clearCue();
        return;
      }
      this.schedulePlaybackClockCheck();
    }, PLAYBACK_CLOCK_POLL_MS);
  }

  private clearCue(): void {
    this.cancelClearTimer();
    this.cueStartMs = null;
    this.cueEndMs = null;
    if (this.overlay) this.overlay.textContent = "";
  }

  private cancelClearTimer(): void {
    if (!this.clearTimer) return;
    clearTimeout(this.clearTimer);
    this.clearTimer = null;
  }
}
