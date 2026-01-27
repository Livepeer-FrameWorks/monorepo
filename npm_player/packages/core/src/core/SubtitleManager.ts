/**
 * SubtitleManager - WebVTT subtitle track management
 *
 * Based on MistMetaPlayer's subtitle handling (wrappers/html5.js, webrtc.js).
 * Manages text tracks on video elements with support for:
 * - Loading WebVTT from MistServer URLs
 * - Multiple subtitle track selection
 * - Sync correction for WebRTC seek offsets
 */

export interface SubtitleTrackInfo {
  /** Track ID (from MistServer) */
  id: string;
  /** Display label */
  label: string;
  /** Language code (e.g., 'en', 'es') */
  lang: string;
  /** Source URL for WebVTT file */
  src: string;
  /** Whether this is the default track */
  default?: boolean;
}

export interface SubtitleManagerConfig {
  /** Base URL for MistServer (for constructing track URLs) */
  mistBaseUrl?: string;
  /** Stream name */
  streamName?: string;
  /** URL append string (auth tokens, etc.) */
  urlAppend?: string;
  /** Debug logging */
  debug?: boolean;
}

/**
 * SubtitleManager handles text track lifecycle on a video element
 */
export class SubtitleManager {
  private video: HTMLVideoElement | null = null;
  private config: SubtitleManagerConfig;
  private currentTrackId: string | null = null;
  private seekOffset = 0;
  private debug: boolean;
  private listeners: Array<() => void> = [];

  constructor(config: SubtitleManagerConfig = {}) {
    this.config = config;
    this.debug = config.debug ?? false;
  }

  /**
   * Attach to a video element
   */
  attach(video: HTMLVideoElement): void {
    this.detach();
    this.video = video;

    // Listen for events that may require sync correction
    const onLoadedData = () => this.correctSubtitleSync();
    const onSeeked = () => this.correctSubtitleSync();

    video.addEventListener("loadeddata", onLoadedData);
    video.addEventListener("seeked", onSeeked);

    this.listeners = [
      () => video.removeEventListener("loadeddata", onLoadedData),
      () => video.removeEventListener("seeked", onSeeked),
    ];
  }

  /**
   * Detach from video element
   */
  detach(): void {
    this.listeners.forEach((fn) => fn());
    this.listeners = [];
    this.removeAllTracks();
    this.video = null;
    this.currentTrackId = null;
  }

  /**
   * Get available text tracks from the video element
   */
  getTextTracks(): TextTrack[] {
    if (!this.video) return [];
    return Array.from(this.video.textTracks);
  }

  /**
   * Get all track elements from the video
   */
  getTrackElements(): HTMLTrackElement[] {
    if (!this.video) return [];
    return Array.from(this.video.querySelectorAll("track"));
  }

  /**
   * Set the active subtitle track
   * Pass null to disable subtitles
   */
  setSubtitle(track: SubtitleTrackInfo | null): void {
    if (!this.video) {
      this.log("Cannot set subtitle: no video element attached");
      return;
    }

    // Remove existing subtitle tracks
    this.removeAllTracks();

    if (!track) {
      this.currentTrackId = null;
      this.log("Subtitles disabled");
      return;
    }

    // Create new track element
    const trackElement = document.createElement("track");
    trackElement.kind = "subtitles";
    trackElement.label = track.label;
    trackElement.srclang = track.lang;
    trackElement.src = this.buildTrackUrl(track.src);
    trackElement.default = true;

    // Set up load handler for sync correction
    trackElement.addEventListener("load", () => {
      this.correctSubtitleSync();
    });

    this.video.appendChild(trackElement);
    this.currentTrackId = track.id;

    // Enable the track
    const textTrack = this.video.textTracks[this.video.textTracks.length - 1];
    if (textTrack) {
      textTrack.mode = "showing";
    }

    this.log(`Subtitle track set: ${track.label} (${track.lang})`);
  }

  /**
   * Build track URL with base URL and append params
   */
  private buildTrackUrl(src: string): string {
    let url = src;

    // If relative URL and base URL provided, construct full URL
    if (!url.startsWith("http") && this.config.mistBaseUrl) {
      const base = this.config.mistBaseUrl.replace(/\/$/, "");
      url = url.startsWith("/") ? `${base}${url}` : `${base}/${url}`;
    }

    // Append URL params if configured
    if (this.config.urlAppend) {
      const separator = url.includes("?") ? "&" : "?";
      url = `${url}${separator}${this.config.urlAppend}`;
    }

    return url;
  }

  /**
   * Create subtitle track info from MistServer track metadata
   */
  static createTrackInfo(
    trackId: string,
    label: string,
    lang: string,
    baseUrl: string,
    streamName: string
  ): SubtitleTrackInfo {
    // MistServer WebVTT URL format
    const src = `${baseUrl}/${streamName}.vtt?track=${trackId}`;
    return { id: trackId, label, lang, src };
  }

  /**
   * Remove all track elements from video
   */
  removeAllTracks(): void {
    if (!this.video) return;

    const tracks = this.video.querySelectorAll("track");
    tracks.forEach((track) => track.remove());
  }

  /**
   * Get currently active track ID
   */
  getCurrentTrackId(): string | null {
    return this.currentTrackId;
  }

  /**
   * Set seek offset for WebRTC sync correction
   * WebRTC playback has a seek offset that needs to be applied to subtitle timing
   */
  setSeekOffset(offset: number): void {
    const oldOffset = this.seekOffset;
    this.seekOffset = offset;

    // Re-sync if offset changed significantly
    if (Math.abs(oldOffset - offset) > 1) {
      this.correctSubtitleSync();
    }
  }

  /**
   * Correct subtitle timing based on seek offset
   * This is needed for WebRTC where video.currentTime doesn't match actual playback position
   */
  private correctSubtitleSync(): void {
    if (!this.video || this.video.textTracks.length === 0) return;

    const textTrack = this.video.textTracks[0];
    if (!textTrack || !textTrack.cues) return;

    const currentOffset = (textTrack as any).currentOffset || 0;

    // Don't bother if change is small
    if (Math.abs(this.seekOffset - currentOffset) < 1) return;

    this.log(`Correcting subtitle sync: offset ${currentOffset} -> ${this.seekOffset}`);

    // Collect and re-add cues with corrected timing
    const newCues: VTTCue[] = [];

    for (let i = textTrack.cues.length - 1; i >= 0; i--) {
      const cue = textTrack.cues[i] as VTTCue;
      textTrack.removeCue(cue);

      // Store original timing if not already stored
      if (!(cue as any).orig) {
        (cue as any).orig = { start: cue.startTime, end: cue.endTime };
      }

      // Apply offset correction
      cue.startTime = (cue as any).orig.start - this.seekOffset;
      cue.endTime = (cue as any).orig.end - this.seekOffset;

      newCues.push(cue);
    }

    // Re-add cues
    for (const cue of newCues) {
      try {
        textTrack.addCue(cue);
      } catch {
        // Ignore errors from invalid cue timing
      }
    }

    (textTrack as any).currentOffset = this.seekOffset;
  }

  /**
   * Parse subtitle tracks from MistServer stream info
   */
  static parseTracksFromStreamInfo(
    streamInfo: {
      meta?: { tracks?: Record<string, { type: string; codec: string; lang?: string }> };
    },
    baseUrl: string,
    streamName: string
  ): SubtitleTrackInfo[] {
    const tracks: SubtitleTrackInfo[] = [];

    if (!streamInfo.meta?.tracks) return tracks;

    for (const [trackId, trackData] of Object.entries(streamInfo.meta.tracks)) {
      if (trackData.type === "meta" && trackData.codec === "subtitle") {
        const lang = trackData.lang || "und";
        const label = lang === "und" ? `Subtitles ${trackId}` : lang.toUpperCase();
        tracks.push(SubtitleManager.createTrackInfo(trackId, label, lang, baseUrl, streamName));
      }
    }

    return tracks;
  }

  /**
   * Debug logging
   */
  private log(message: string): void {
    if (this.debug) {
      console.debug(`[SubtitleManager] ${message}`);
    }
  }

  /**
   * Cleanup
   */
  destroy(): void {
    this.detach();
  }
}

export default SubtitleManager;
