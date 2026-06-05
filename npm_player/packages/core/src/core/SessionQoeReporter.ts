import type { ContentType } from "../types";

/**
 * SessionQoeReporter — measures viewer-EXPERIENCED playback QoE (rebuffering,
 * frame drops, watch time) and emits it as a beacon to Bridge.
 *
 * This is the authoritative source for what the viewer actually saw. The origin
 * (server stream_health) can't observe it: for HLS/DASH the player prefetches
 * segments, so the origin buffer reads healthy while the viewer's media buffer
 * underruns on a bad last-mile link.
 *
 * Counters are shaped as additive deltas (delta since the previous flush), and
 * periodic heartbeats mean a lost final beacon still leaves earlier partial
 * data under sum(). beacon_seq increments per beacon and is_final marks the
 * last beacon when the player can observe session end.
 *
 * The crucial correctness rules, all enforced here at instrumentation time:
 *   - The rebuffer-ratio denominator is genuine watch time = the union of
 *     video.played, never wall-clock or currentTime delta.
 *   - A `waiting` event is classified: initial buffering (pre-first-frame),
 *     seek-induced, pause, or genuine rebuffer — only the last counts.
 *   - getVideoPlaybackQuality counters are cumulative & non-resettable, so frame
 *     stats are emitted as per-window deltas.
 */

export interface SessionQoeDelta {
  contentId: string;
  sessionId: string;
  contentType?: ContentType;
  isLive?: boolean;
  playerType?: string;
  protocol?: string;
  playerVersion?: string;
  connectionType?: string;

  /** Increments per beacon within a session; (contentId, sessionId, beaconSeq) is the dedupe key. */
  beaconSeq: number;
  isFinal: boolean;
  flushReason: string;

  /** Genuine watch time this window (union of video.played growth), ms. The ratio denominator. */
  playedMs: number;
  /** Genuine rebuffering only (initial buffering, seeks, pauses excluded), ms. */
  rebufferMs: number;
  rebufferCount: number;
  /** Seek-induced waiting, diagnostic; NOT a rebuffer. */
  seekWaitMs: number;

  frameStatsSupported: boolean;
  framesDecoded: number;
  framesDropped: number;
  framesCorrupted: number;

  firstFrame: boolean;
  fatalError: boolean;
  errorCode?: string;

  /** Σ(selected_bitrate_bps × played_seconds) this window; avg = ÷ playedMs/1000 at read. */
  bitrateBpsSeconds: number;
  abrUpswitchCount: number;
  abrDownswitchCount: number;
  /**
   * Play was intended this session; EBVS = playIntent && !firstFrame at read. Scope
   * is post-player-ready (this reporter starts at onReady) — pre-ready startup
   * failures are the boot trace's video-start-failure domain, not EBVS.
   */
  playIntent: boolean;
  /** Live-edge latency snapshot (player latency, NOT glass-to-glass), ms; 0 for VOD/unknown. */
  liveEdgeLatencyMs: number;

  // VOD retention (omitted for live). Fixed-width buckets along the asset timeline.
  // retentionBuckets/retentionSecondsWatched are sparse parallel arrays of per-bucket
  // watched-seconds DELTAS for this window → the "most replayed" WATCH-DENSITY curve
  // (Σ seconds per bucket). maxBucketReached is the furthest bucket the playhead
  // reached this session (cumulative; server takes the per-session max) → the input
  // to the true AUDIENCE-RETENTION curve (sessions whose reach ≥ T ÷ total). These are
  // different aggregations: a seek-to-end advances reach without adding watch density.
  bucketWidthS?: number;
  assetDurationS?: number;
  retentionBuckets?: number[];
  retentionSecondsWatched?: number[];
  maxBucketReached?: number;
}

export interface SessionQoeReporterConfig {
  contentId: string;
  /** Correlation id; share the boot trace's sessionId so the two beacons join. */
  sessionId: string;
  contentType?: ContentType;
  isLive?: boolean;
  playerType?: string;
  protocol?: string;
  playerVersion?: string;
  /** Invoked per emitted beacon (heartbeats + final). */
  onFlush: (delta: SessionQoeDelta) => void;
  /**
   * Best-effort async sampler for selected bitrate (bps) and live-edge latency
   * (ms), polled on the stats tick. Omit to skip delivery-quality capture.
   */
  statsProvider?: () => Promise<{ bitrateBps?: number; liveEdgeMs?: number } | null>;
  /** Debug logger (no-op when omitted). */
  log?: (message: string) => void;
}

/** A `waiting` interval is one of these; only "rebuffer" counts toward QoE. */
type StallKind = "rebuffer" | "seek";

/** Grace after `seeked` during which a `waiting` is still attributed to the seek. */
const SEEK_GRACE_MS = 1000;

/** Stats sampling cadence (bitrate/live-edge). */
const STATS_INTERVAL_MS = 5000;
/** Heartbeat beacon every N stats ticks → ~30s. Keeps well under the 120/min/IP cap. */
const HEARTBEAT_TICKS = 6;

type FrameCapable = HTMLVideoElement & {
  getVideoPlaybackQuality?: () => VideoPlaybackQuality;
};

function now(): number {
  return typeof performance !== "undefined" && typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}

/** Sum of video.played ranges (seconds → ms) = media actually rendered this session. */
function playedUnionMs(video: HTMLVideoElement): number {
  const played = video.played;
  if (!played || played.length === 0) return 0;
  let total = 0;
  for (let i = 0; i < played.length; i++) {
    total += played.end(i) - played.start(i);
  }
  return Math.max(0, Math.round(total * 1000));
}

/**
 * Fixed bucket width (seconds) by asset duration tier. Fixed WIDTH (not a fixed
 * bucket count) keeps the bucket→timestamp mapping asset-independent so curves
 * aggregate across assets; the tiers bound a long asset to a few hundred buckets.
 */
function bucketWidthForDuration(durationSec: number): number {
  if (durationSec <= 600) return 2; // ≤10 min
  if (durationSec <= 3600) return 5; // ≤1 h
  if (durationSec <= 10800) return 15; // ≤3 h
  return 30;
}

/** Cumulative watched seconds per fixed-width bucket, from the video.played union. */
function playedByBucket(video: HTMLVideoElement, widthSec: number): Map<number, number> {
  const out = new Map<number, number>();
  const played = video.played;
  if (!played || played.length === 0 || widthSec <= 0) return out;
  for (let i = 0; i < played.length; i++) {
    const start = played.start(i);
    const end = played.end(i);
    // Walk the range bucket by bucket, crediting each its overlap with [start,end).
    let b = Math.floor(start / widthSec);
    while (b * widthSec < end) {
      const lo = Math.max(start, b * widthSec);
      const hi = Math.min(end, (b + 1) * widthSec);
      if (hi > lo) out.set(b, (out.get(b) ?? 0) + (hi - lo));
      b++;
    }
  }
  return out;
}

export class SessionQoeReporter {
  private config: SessionQoeReporterConfig;
  private video: FrameCapable | null = null;
  private cleanup: Array<() => void> = []; // session-level (interval, window, document)
  private elementCleanup: Array<() => void> = []; // per-video-element listeners
  private sessionSetup = false;
  private completed = false;
  private beaconSeq = 0;

  // Watch time + frame counters banked from prior video elements (player fallback /
  // element swap), so the played-union denominator and the cumulative frame counters
  // stay continuous across the swap (a fresh element restarts both near zero).
  private bankedPlayedMs = 0;
  private bankedFrames = { decoded: 0, dropped: 0, corrupted: 0 };
  // Furthest playhead position (s) reached this session — the reach/retention input.
  private maxPlayheadSec = 0;

  // First painted frame gates rebuffer classification: a `waiting` before it is
  // initial buffering (boot's prebuffer), not a rebuffer.
  private firstFrame = false;

  // Open-stall state.
  private stallKind: StallKind | null = null;
  private stallStart = 0;

  // Seek window.
  private seekActive = false;
  private lastSeekedAt = -Infinity;

  // Cumulative session totals (deltas are computed against lastFlushed on flush).
  private rebufferMs = 0;
  private rebufferCount = 0;
  private seekWaitMs = 0;
  private fatalError = false;
  private errorCode: string | undefined;

  // Delivery quality. bitrateBpsSeconds integrates the current selected bitrate
  // over PLAYED time (same denominator as the ratio), advancing the anchor at
  // each switch and flush; ABR counts split by direction of the bitrate change.
  private currentBitrate = 0;
  private bitrateAnchorPlayedMs = 0;
  private bitrateBpsSeconds = 0;
  private abrUpswitchCount = 0;
  private abrDownswitchCount = 0;
  private lastLiveEdgeMs = 0;
  private playIntent = false;

  // VOD retention histogram. bucketWidthS is fixed once the asset duration is
  // known; lastBucketWatched holds the cumulative per-bucket seconds at the last
  // flush so each beacon can emit deltas.
  private bucketWidthS = 0;
  private lastBucketWatched = new Map<number, number>();

  // Heartbeat/stats interval bookkeeping.
  private tickCount = 0;

  private lastFlushed = {
    playedMs: 0,
    rebufferMs: 0,
    rebufferCount: 0,
    seekWaitMs: 0,
    framesDecoded: 0,
    framesDropped: 0,
    framesCorrupted: 0,
    bitrateBpsSeconds: 0,
    abrUpswitchCount: 0,
    abrDownswitchCount: 0,
    maxBucketReached: 0,
    playIntent: false,
    firstFrame: false,
    fatalError: false,
  };

  constructor(config: SessionQoeReporterConfig) {
    this.config = config;
  }

  /** Late-resolved context (player/protocol chosen after construction). */
  setContext(ctx: { playerType?: string; protocol?: string; isLive?: boolean }): void {
    if (ctx.playerType) this.config.playerType = ctx.playerType;
    if (ctx.protocol) this.config.protocol = ctx.protocol;
    if (typeof ctx.isLive === "boolean") this.config.isLive = ctx.isLive;
  }

  /**
   * Attach to (or re-attach to) the media element and begin measuring. Idempotent
   * for the same element. On a DIFFERENT element (player fallback / element swap)
   * it banks the old element's watch time, drops the old element's listeners, and
   * rebinds to the new one — so metrics don't go deaf after a fallback. Session
   * state (rebuffers, frames, bitrate, reach) carries across the swap.
   */
  attach(video: HTMLVideoElement): void {
    if (this.completed || this.video === video) return;

    if (this.video) {
      // Element swap (player fallback). Flush the old element's pending deltas FIRST
      // — including its watched-bucket density — so a swap before the next heartbeat
      // doesn't lose them; flush reads the still-current old element.
      if (this.hasActivitySinceFlush()) this.flush("element_swap", false);
      // Lock in the old element's played union AND frame counters so both stay
      // continuous (the new element restarts near zero), drop its listeners, and
      // reset the per-element retention baseline so its watched buckets emit fresh.
      this.bankedPlayedMs += playedUnionMs(this.video);
      const raw = this.rawFrames();
      if (raw) {
        this.bankedFrames.decoded += raw.decoded;
        this.bankedFrames.dropped += raw.dropped;
        this.bankedFrames.corrupted += raw.corrupted;
      }
      this.elementCleanup.forEach((fn) => fn());
      this.elementCleanup = [];
      this.lastBucketWatched = new Map();
    }

    this.video = video as FrameCapable;
    this.bindElement(video);

    if (!this.sessionSetup) {
      this.sessionSetup = true;
      this.setupSessionListeners();
    }
  }

  /** Bind the per-element media listeners (re-bound on each element swap). */
  private bindElement(video: HTMLVideoElement): void {
    const onPlaying = () => {
      const wasFirstFrame = this.firstFrame;
      this.firstFrame = true;
      this.closeStall();
      // If a beacon already went out reporting this session as not-yet-first-frame
      // (e.g. an EBVS intent heartbeat), flush the first-frame transition NOW so a
      // crash before the final can't leave the read layer counting a false EBVS.
      if (!wasFirstFrame && this.beaconSeq > 0 && !this.lastFlushed.firstFrame) {
        this.flush("first_frame", false);
      }
    };
    const onTimeUpdate = () => {
      this.maxPlayheadSec = Math.max(this.maxPlayheadSec, video.currentTime || 0);
      // Media advanced — any open stall has resolved.
      if (this.stallKind) this.closeStall();
    };
    const onWaiting = () => this.onWaiting();
    const onPause = () => this.closeStall();
    const onSeeking = () => {
      this.seekActive = true;
    };
    const onSeeked = () => {
      this.seekActive = false;
      this.lastSeekedAt = now();
      this.maxPlayheadSec = Math.max(this.maxPlayheadSec, video.currentTime || 0);
      // A seek that interrupted a genuine rebuffer ends that stall.
      this.closeStall();
    };
    // No direct video 'error' listener: a raw media-element error may still recover
    // via player fallback, so marking it fatal here would over-count. Fatal recording
    // is centralized on recordFatalError(), which the controller calls only on the
    // terminal (fallback-exhausted) path — for native and HLS/DASH errors alike.

    video.addEventListener("playing", onPlaying);
    video.addEventListener("timeupdate", onTimeUpdate);
    video.addEventListener("waiting", onWaiting);
    video.addEventListener("pause", onPause);
    video.addEventListener("seeking", onSeeking);
    video.addEventListener("seeked", onSeeked);
    this.elementCleanup.push(
      () => video.removeEventListener("playing", onPlaying),
      () => video.removeEventListener("timeupdate", onTimeUpdate),
      () => video.removeEventListener("waiting", onWaiting),
      () => video.removeEventListener("pause", onPause),
      () => video.removeEventListener("seeking", onSeeking),
      () => video.removeEventListener("seeked", onSeeked)
    );
  }

  /** Set up session-level listeners + the heartbeat interval (once per session). */
  private setupSessionListeners(): void {
    const onVisibility = () => {
      if (typeof document === "undefined" || !document.hidden) return;
      // A backgrounded tab throttles timers; an open stall while hidden is not a
      // real viewer stall. Close it so suspended time isn't counted as rebuffer.
      this.closeStall();
      // Flush a NON-terminal delta so a backgrounded/abandoned session still
      // contributes — the upward-bias fix. The user may return; more deltas
      // follow, and a final beacon still closes the session on pagehide/teardown.
      if (this.hasActivitySinceFlush()) this.flush("visibility_hidden", false);
    };
    const onPageHide = () => this.finalize("pagehide");

    if (typeof document !== "undefined") {
      document.addEventListener("visibilitychange", onVisibility);
      this.cleanup.push(() => document.removeEventListener("visibilitychange", onVisibility));
    }
    // pagehide is the reliable unload signal (mobile-safe). It guarantees the
    // final beacon fires when the tab is closed without an explicit teardown.
    if (typeof window !== "undefined") {
      window.addEventListener("pagehide", onPageHide);
      this.cleanup.push(() => window.removeEventListener("pagehide", onPageHide));
    }

    // Periodic stats sampling + heartbeat beacon. A random initial tick offset
    // jitters the heartbeat boundary across sessions so many viewers behind one
    // NAT don't beacon in lockstep against the limiter.
    if (typeof setInterval === "function") {
      this.tickCount = Math.floor(
        (typeof Math !== "undefined" ? Math.random() : 0) * HEARTBEAT_TICKS
      );
      const timer = setInterval(() => void this.onTick(), STATS_INTERVAL_MS);
      this.cleanup.push(() => clearInterval(timer));
    }
  }

  /** Total played-union watch time across all elements this session (ms). */
  private playedMsTotal(): number {
    return this.bankedPlayedMs + (this.video ? playedUnionMs(this.video) : 0);
  }

  /** Record a player-level fatal error (e.g. HLS.js fatal) after first frame. */
  recordFatalError(code: string): void {
    if (this.completed || !this.firstFrame) return;
    this.fatalError = true;
    this.errorCode = code;
    // Flush the fatal immediately (non-terminal) so it is durable even if the
    // final/pagehide beacon never makes it out (e.g. the tab crashes on the error).
    this.flush("error", false);
  }

  /** Mark that playback was intended (EBVS denominator). */
  markPlayIntent(): void {
    if (!this.completed) this.playIntent = true;
  }

  /**
   * Feed the currently selected rendition bitrate (bps). Closes out the prior
   * bitrate's played-time segment and counts the switch by direction. Coarse
   * sampling (no per-switch event hook) catches net switches; rapid oscillation
   * between samples is undercounted — acceptable for the sample-first approach.
   */
  sampleBitrate(bitrateBps: number): void {
    if (this.completed || !this.video || bitrateBps <= 0) return;
    if (this.currentBitrate === 0) {
      this.currentBitrate = bitrateBps;
      this.bitrateAnchorPlayedMs = this.playedMsTotal();
      return;
    }
    if (bitrateBps === this.currentBitrate) return;
    this.integrateBitrate();
    if (bitrateBps > this.currentBitrate) this.abrUpswitchCount++;
    else this.abrDownswitchCount++;
    this.currentBitrate = bitrateBps;
  }

  private integrateBitrate(): void {
    if (!this.video || this.currentBitrate <= 0) return;
    const playedNow = this.playedMsTotal();
    const dMs = playedNow - this.bitrateAnchorPlayedMs;
    if (dMs > 0) {
      this.bitrateBpsSeconds += Math.round(this.currentBitrate * (dMs / 1000));
    }
    this.bitrateAnchorPlayedMs = playedNow;
  }

  private async onTick(): Promise<void> {
    if (this.completed) return;
    if (this.config.statsProvider) {
      try {
        const s = await this.config.statsProvider();
        if (s) {
          if (typeof s.bitrateBps === "number" && s.bitrateBps > 0)
            this.sampleBitrate(s.bitrateBps);
          if (typeof s.liveEdgeMs === "number")
            this.lastLiveEdgeMs = Math.max(0, Math.round(s.liveEdgeMs));
        }
      } catch {
        // stats are best-effort
      }
    }
    if (this.completed) return; // could have finalized during the await
    this.tickCount++;
    if (this.tickCount % HEARTBEAT_TICKS === 0 && this.hasActivitySinceFlush()) {
      this.flush("heartbeat", false);
    }
  }

  /** Furthest bucket the playhead reached (VOD), 0 for live / unknown timeline. */
  private currentReachBucket(): number {
    const video = this.video;
    if (!video || this.config.isLive) return 0;
    const duration = video.duration;
    if (!Number.isFinite(duration) || duration <= 0) return 0;
    // Resolve the bucket width here too (not only in flush), so the heartbeat gate
    // can see reach advance before the first flush sets it.
    if (this.bucketWidthS === 0) this.bucketWidthS = bucketWidthForDuration(duration);
    return Math.floor(this.maxPlayheadSec / this.bucketWidthS);
  }

  /**
   * True if anything worth a beacon happened since the last flush (suppresses
   * idle/paused spam). Beyond played/rebuffer/ABR, a newly-set play intent and an
   * advanced VOD reach also count — otherwise EBVS (play-intended, no first frame)
   * and seek-only VOD reach would survive only in the final beacon, which is exactly
   * the lossy case the periodic/visibility flush exists to cover.
   */
  private hasActivitySinceFlush(): boolean {
    if (!this.video) return false;
    return (
      this.playedMsTotal() > this.lastFlushed.playedMs ||
      this.rebufferMs > this.lastFlushed.rebufferMs ||
      this.abrUpswitchCount + this.abrDownswitchCount >
        this.lastFlushed.abrUpswitchCount + this.lastFlushed.abrDownswitchCount ||
      this.currentReachBucket() > this.lastFlushed.maxBucketReached ||
      (this.playIntent && !this.lastFlushed.playIntent) ||
      this.firstFrame !== this.lastFlushed.firstFrame ||
      this.fatalError !== this.lastFlushed.fatalError
    );
  }

  /** Emit the final beacon and stop measuring. Idempotent — first call wins. */
  finalize(reason: string): void {
    if (this.completed) return;
    this.closeStall();
    this.flush(reason, true);
    this.completed = true;
    this.elementCleanup.forEach((fn) => fn());
    this.elementCleanup = [];
    this.cleanup.forEach((fn) => fn());
    this.cleanup = [];
    this.video = null;
  }

  isCompleted(): boolean {
    return this.completed;
  }

  private onWaiting(): void {
    if (this.completed || !this.video) return;
    if (!this.firstFrame) return; // initial buffering — boot's prebuffer, not a rebuffer
    if (this.video.paused) return; // pause is not a stall
    const seeking = this.seekActive || now() - this.lastSeekedAt < SEEK_GRACE_MS;
    if (seeking) {
      this.openStall("seek");
      return;
    }
    if (typeof document !== "undefined" && document.hidden) return; // backgrounded tab
    this.openStall("rebuffer");
  }

  private openStall(kind: StallKind): void {
    if (this.stallKind) return; // a stall is already open
    this.stallKind = kind;
    this.stallStart = now();
    if (kind === "rebuffer") this.rebufferCount++;
  }

  private closeStall(): void {
    if (!this.stallKind) return;
    const dur = Math.max(0, now() - this.stallStart);
    if (this.stallKind === "rebuffer") {
      this.rebufferMs += dur;
    } else {
      this.seekWaitMs += dur;
    }
    this.stallKind = null;
    this.stallStart = 0;
  }

  /** Current element's raw cumulative frame counters, or null if unsupported. */
  private rawFrames(): { decoded: number; dropped: number; corrupted: number } | null {
    const video = this.video;
    if (!video || typeof video.getVideoPlaybackQuality !== "function") return null;
    const q = video.getVideoPlaybackQuality();
    return {
      decoded: q.totalVideoFrames ?? 0,
      dropped: q.droppedVideoFrames ?? 0,
      corrupted:
        (q as VideoPlaybackQuality & { corruptedVideoFrames?: number }).corruptedVideoFrames ?? 0,
    };
  }

  private frameStats(): {
    supported: boolean;
    decoded: number;
    dropped: number;
    corrupted: number;
  } {
    const raw = this.rawFrames();
    // Banked counters from prior elements keep the totals continuous across an
    // element swap (a fresh element's getVideoPlaybackQuality restarts near zero).
    const decoded = this.bankedFrames.decoded + (raw?.decoded ?? 0);
    // A platform that never reports frames (decoded stays 0) is "unsupported",
    // not "perfect" — callers must not read 0/0 as flawless rendering.
    return {
      supported: decoded > 0,
      decoded,
      dropped: this.bankedFrames.dropped + (raw?.dropped ?? 0),
      corrupted: this.bankedFrames.corrupted + (raw?.corrupted ?? 0),
    };
  }

  /**
   * VOD-only per-bucket watched-seconds DELTA since the last flush. Returns null
   * for live or until the asset duration is known. Sparse — only buckets that
   * gained watch time are emitted.
   */
  private retentionDelta(): {
    bucketWidthS: number;
    assetDurationS: number;
    buckets: number[];
    seconds: number[];
  } | null {
    const video = this.video;
    if (!video || this.config.isLive) return null;
    const duration = video.duration;
    if (!Number.isFinite(duration) || duration <= 0) return null;

    if (this.bucketWidthS === 0) this.bucketWidthS = bucketWidthForDuration(duration);
    const current = playedByBucket(video, this.bucketWidthS);

    const buckets: number[] = [];
    const seconds: number[] = [];
    for (const [bucket, total] of current) {
      const delta = total - (this.lastBucketWatched.get(bucket) ?? 0);
      if (delta > 0.001) {
        buckets.push(bucket);
        seconds.push(Math.round(delta * 1000) / 1000);
      }
    }
    this.lastBucketWatched = current;
    if (buckets.length === 0) return null;
    return {
      bucketWidthS: this.bucketWidthS,
      assetDurationS: Math.round(duration),
      buckets,
      seconds,
    };
  }

  private flush(reason: string, isFinal: boolean): void {
    const video = this.video;
    if (!video) return;

    // Bring the bitrate integral current to this instant before snapshotting.
    this.integrateBitrate();

    const playedTotal = this.playedMsTotal();
    const frames = this.frameStats();
    const retention = this.retentionDelta(); // sets this.bucketWidthS for VOD
    // VOD timeline geometry, known once duration resolves (independent of whether
    // anything was watched). Its presence is the read-layer's "this is a real VOD
    // reach sample" gate; null for live / pre-duration.
    const vodGeometry =
      this.bucketWidthS > 0 && video.duration > 0 && Number.isFinite(video.duration)
        ? { bucketWidthS: this.bucketWidthS, assetDurationS: Math.round(video.duration) }
        : null;
    // Furthest bucket the playhead reached this session — the reach/retention input,
    // distinct from watch density (a seek-to-end raises reach without watch density).
    const maxBucketReached = this.currentReachBucket();

    const delta: SessionQoeDelta = {
      contentId: this.config.contentId,
      sessionId: this.config.sessionId,
      contentType: this.config.contentType,
      isLive: this.config.isLive,
      playerType: this.config.playerType,
      protocol: this.config.protocol,
      playerVersion: this.config.playerVersion,
      connectionType: this.connectionType(),

      beaconSeq: this.beaconSeq,
      isFinal,
      flushReason: reason,

      playedMs: Math.max(0, playedTotal - this.lastFlushed.playedMs),
      rebufferMs: Math.max(0, this.rebufferMs - this.lastFlushed.rebufferMs),
      rebufferCount: Math.max(0, this.rebufferCount - this.lastFlushed.rebufferCount),
      seekWaitMs: Math.max(0, this.seekWaitMs - this.lastFlushed.seekWaitMs),

      frameStatsSupported: frames.supported,
      framesDecoded: Math.max(0, frames.decoded - this.lastFlushed.framesDecoded),
      framesDropped: Math.max(0, frames.dropped - this.lastFlushed.framesDropped),
      framesCorrupted: Math.max(0, frames.corrupted - this.lastFlushed.framesCorrupted),

      firstFrame: this.firstFrame,
      fatalError: this.fatalError,
      errorCode: this.errorCode,

      bitrateBpsSeconds: Math.max(0, this.bitrateBpsSeconds - this.lastFlushed.bitrateBpsSeconds),
      abrUpswitchCount: Math.max(0, this.abrUpswitchCount - this.lastFlushed.abrUpswitchCount),
      abrDownswitchCount: Math.max(
        0,
        this.abrDownswitchCount - this.lastFlushed.abrDownswitchCount
      ),
      playIntent: this.playIntent,
      liveEdgeLatencyMs: this.lastLiveEdgeMs,

      // VOD geometry + reach are emitted whenever the timeline is known (a positive
      // bucketWidthS is the presence bit the read layer gates on), independent of
      // whether any new buckets were watched this window — so a seek-only session
      // still contributes geometry + reach. The histogram arrays stay sparse.
      bucketWidthS: vodGeometry?.bucketWidthS,
      assetDurationS: vodGeometry?.assetDurationS,
      retentionBuckets: retention?.buckets,
      retentionSecondsWatched: retention?.seconds,
      maxBucketReached: vodGeometry ? maxBucketReached : undefined,
    };

    this.lastFlushed = {
      playedMs: playedTotal,
      rebufferMs: this.rebufferMs,
      rebufferCount: this.rebufferCount,
      seekWaitMs: this.seekWaitMs,
      framesDecoded: frames.decoded,
      framesDropped: frames.dropped,
      framesCorrupted: frames.corrupted,
      bitrateBpsSeconds: this.bitrateBpsSeconds,
      abrUpswitchCount: this.abrUpswitchCount,
      abrDownswitchCount: this.abrDownswitchCount,
      maxBucketReached,
      playIntent: this.playIntent,
      firstFrame: this.firstFrame,
      fatalError: this.fatalError,
    };
    this.beaconSeq++;

    if (this.config.log) {
      this.config.log(
        `[qoe] ${reason} played=${delta.playedMs}ms rebuffer=${delta.rebufferMs}ms x${delta.rebufferCount}`
      );
    }
    try {
      this.config.onFlush(delta);
    } catch {
      // never let a consumer callback break teardown
    }
  }

  private connectionType(): string | undefined {
    const conn = (navigator as unknown as { connection?: { effectiveType?: string } } | undefined)
      ?.connection;
    return conn?.effectiveType;
  }
}

export default SessionQoeReporter;
