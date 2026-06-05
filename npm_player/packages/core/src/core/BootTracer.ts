import type { ContentEndpoints, ContentType, PlayerState, PlayerStateContext } from "../types";

/**
 * BootTracer — measures the player startup waterfall (time-to-first-frame split
 * into spans) plus Resource Timing for the manifest/segment/CDN behaviour behind
 * the single visible `gateway_loading` label.
 *
 * It is local-only: it logs in debug mode and reports a finished trace through
 * `onComplete`. It never performs network I/O itself.
 */

/** Ordered boot milestones. Each is recorded at most once (first wins). */
export type BootMark =
  | "boot_start"
  | "gateway_loading"
  | "gateway_resolved"
  | "gateway_ready"
  | "selecting_player"
  | "connecting"
  | "buffering"
  | "first_frame";

export type BootOutcome = "success" | "error" | "abandoned";

export type BootResourceKind = "manifest" | "first_segment" | "mist_json" | "poster" | "other";

export interface BootResourceTiming {
  kind: BootResourceKind;
  url: string;
  /** responseStart - requestStart (ms). */
  ttfbMs: number;
  /** Full request duration (ms). */
  durationMs: number;
  transferSize: number;
  encodedBodySize: number;
  decodedBodySize: number;
  /** Cache disposition — only readable for player-owned requests (e.g. Mist JSON). */
  cacheStatus?: string;
  ageSeconds?: number;
  serverTiming?: Array<{ name: string; duration?: number; description?: string }>;
}

export interface BootSpans {
  /** GraphQL resolve only (gateway_loading → gateway_resolved). */
  gatewayResolveMs?: number;
  /** Mist json_*.js hydration (gateway_resolved → gateway_ready). */
  mistHydrateMs?: number;
  /** Player/source selection (selecting_player → connecting). */
  playerSelectMs?: number;
  /** Player init/network connect (connecting → buffering/first_frame). */
  connectMs?: number;
  /** Initial buffering (buffering → first_frame). */
  prebufferMs?: number;
}

export interface BootTrace {
  traceId: string;
  sessionId: string;
  contentId: string;
  contentType?: ContentType;
  isLive?: boolean;
  outcome: BootOutcome;
  errorCode?: string;
  playerType?: string;
  protocol?: string;
  playerVersion?: string;
  /** navigator.connection.effectiveType bucket, when available. */
  connectionType?: string;
  /** boot_start → first_frame (ms). Undefined when first frame was never reached. */
  totalTtfMs?: number;
  spans: BootSpans;
  manifestUrl?: string;
  manifestMs?: number;
  firstSegmentUrl?: string;
  firstSegmentMs?: number;
  resources: BootResourceTiming[];
}

export interface BootTracerConfig {
  contentId: string;
  contentType?: ContentType;
  playerVersion?: string;
  /** Invoked exactly once when the trace finalizes. */
  onComplete: (trace: BootTrace) => void;
  /** Debug logger (no-op when omitted). */
  log?: (message: string) => void;
  /** Resolved endpoints, read at finalize to scope Resource Timing matching. */
  getEndpoints?: () => ContentEndpoints | null;
}

type VideoFrameCapable = HTMLVideoElement & {
  requestVideoFrameCallback?: (cb: () => void) => number;
};

function randomHex(bytes: number): string {
  const buf = new Uint8Array(bytes);
  crypto.getRandomValues(buf);
  return Array.from(buf, (b) => b.toString(16).padStart(2, "0")).join("");
}

/** Correlation id minted client-side. NOT a canonical dedup key — the backend mints that. */
function generateId(): string {
  return `${Date.now().toString(36)}-${randomHex(8)}`;
}

function now(): number {
  return typeof performance !== "undefined" && typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}

function classifyResource(url: string): BootResourceKind {
  const lower = url.toLowerCase();
  if (lower.includes("/json_") || /json_[^/]+\.js(\?|$)/.test(lower)) return "mist_json";
  if (lower.includes(".m3u8") || lower.includes(".mpd")) return "manifest";
  if (lower.includes("poster") || lower.includes("sprite")) return "poster";
  if (/\.(ts|m4s|mp4|webm|cmfv|cmfa)(\?|$)/.test(lower)) return "first_segment";
  return "other";
}

/**
 * Drop query string and fragment from a URL before it leaves the browser.
 * Playback URLs carry signed `?jwt=` / access tokens (see withPlaybackJWT); only
 * the scheme://host/path is useful for diagnostics and safe to persist.
 */
function redactUrl(url: string): string {
  return url.split(/[?#]/)[0];
}

export class BootTracer {
  private config: BootTracerConfig;
  private marks = new Map<BootMark, number>();
  private completed = false;
  private errorCode: string | undefined;
  private playerType: string | undefined;
  private protocol: string | undefined;
  private isLive: boolean | undefined;
  private video: VideoFrameCapable | null = null;
  private frameCleanup: Array<() => void> = [];
  private ownedResponses = new Map<
    string,
    Pick<BootResourceTiming, "cacheStatus" | "ageSeconds">
  >();
  readonly traceId = generateId();
  readonly sessionId = generateId();

  constructor(config: BootTracerConfig) {
    this.config = config;
    this.mark("boot_start");
  }

  /** Record a milestone the first time it occurs. */
  mark(name: BootMark): void {
    if (this.completed || this.marks.has(name)) return;
    this.marks.set(name, now());
  }

  /** Map a PlayerController state transition to a boot mark + captured context. */
  onState(state: PlayerState, context?: PlayerStateContext): void {
    if (this.completed) return;
    switch (state) {
      case "booting":
        this.mark("boot_start");
        break;
      case "gateway_loading":
        this.mark("gateway_loading");
        break;
      case "gateway_ready":
        this.mark("gateway_ready");
        break;
      case "selecting_player":
        this.mark("selecting_player");
        break;
      case "connecting":
        this.mark("connecting");
        if (context?.selectedPlayer) this.playerType = context.selectedPlayer;
        if (context?.selectedProtocol) this.protocol = context.selectedProtocol;
        break;
      case "buffering":
        this.mark("buffering");
        break;
      case "gateway_error":
      case "error":
      case "no_endpoint":
        this.errorCode = context?.error || context?.reason || state;
        break;
      default:
        break;
    }
  }

  setContext(ctx: { playerType?: string; protocol?: string; isLive?: boolean }): void {
    if (ctx.playerType) this.playerType = ctx.playerType;
    if (ctx.protocol) this.protocol = ctx.protocol;
    if (typeof ctx.isLive === "boolean") this.isLive = ctx.isLive;
  }

  recordError(code: string): void {
    if (!this.completed) this.errorCode = code;
  }

  /** Cache headers from a request the player itself issued (e.g. Mist JSON). */
  recordOwnedResponse(url: string, headers: Headers): void {
    if (this.completed) return;
    const ageHeader = headers.get("age");
    this.ownedResponses.set(url, {
      cacheStatus:
        headers.get("x-cache") ||
        headers.get("cf-cache-status") ||
        headers.get("x-cache-status") ||
        undefined,
      ageSeconds: ageHeader != null ? Number(ageHeader) : undefined,
    });
  }

  /**
   * Watch the video element for the first painted frame. Prefers
   * requestVideoFrameCallback (true first frame); falls back to playing/canplay.
   */
  attachVideo(video: HTMLVideoElement): void {
    if (this.completed || this.video) return;
    this.video = video as VideoFrameCapable;

    const onFirstFrame = () => {
      this.mark("first_frame");
      this.finalize("success");
    };

    if (typeof this.video.requestVideoFrameCallback === "function") {
      this.video.requestVideoFrameCallback(onFirstFrame);
      return;
    }

    const handler = () => onFirstFrame();
    video.addEventListener("playing", handler, { once: true });
    video.addEventListener("canplay", handler, { once: true });
    this.frameCleanup.push(() => video.removeEventListener("playing", handler));
    this.frameCleanup.push(() => video.removeEventListener("canplay", handler));
  }

  /** Terminate the trace. Idempotent — the first call wins. */
  finalize(outcome: BootOutcome): void {
    if (this.completed) return;
    this.completed = true;
    this.frameCleanup.forEach((fn) => fn());
    this.frameCleanup = [];

    const trace = this.buildTrace(outcome);
    if (this.config.log) {
      this.config.log(
        `[boot] ${outcome} ttf=${trace.totalTtfMs ?? "-"}ms ${JSON.stringify(trace.spans)}`
      );
    }
    try {
      this.config.onComplete(trace);
    } catch {
      // never let a consumer callback break teardown
    }
  }

  /** Finalize as abandoned/error if a first frame never arrived (called on teardown). */
  abandon(): void {
    if (this.completed) return;
    this.finalize(this.errorCode ? "error" : "abandoned");
  }

  /**
   * Drop the trace WITHOUT emitting — used when a boot is superseded (e.g. a
   * fresh trace re-anchored on offline→online recovery). Marks completed so any
   * pending requestVideoFrameCallback / media listeners no-op, and removes the
   * listeners, but never calls onComplete.
   */
  cancel(): void {
    if (this.completed) return;
    this.completed = true;
    this.frameCleanup.forEach((fn) => fn());
    this.frameCleanup = [];
  }

  isCompleted(): boolean {
    return this.completed;
  }

  private span(from: BootMark, to: BootMark): number | undefined {
    const a = this.marks.get(from);
    const b = this.marks.get(to);
    if (a == null || b == null || b < a) return undefined;
    return Math.round(b - a);
  }

  private buildTrace(outcome: BootOutcome): BootTrace {
    const spans: BootSpans = {
      gatewayResolveMs: this.span("gateway_loading", "gateway_resolved"),
      mistHydrateMs: this.span("gateway_resolved", "gateway_ready"),
      playerSelectMs: this.span("selecting_player", "connecting"),
      connectMs: this.span("connecting", "buffering") ?? this.span("connecting", "first_frame"),
      prebufferMs: this.span("buffering", "first_frame"),
    };

    const resources = this.collectResources();
    const manifest = resources.find((r) => r.kind === "manifest");
    const firstSegment = resources.find((r) => r.kind === "first_segment");

    return {
      traceId: this.traceId,
      sessionId: this.sessionId,
      contentId: this.config.contentId,
      contentType: this.config.contentType,
      isLive: this.isLive,
      outcome,
      errorCode: this.errorCode,
      playerType: this.playerType,
      protocol: this.protocol,
      playerVersion: this.config.playerVersion,
      connectionType: this.connectionType(),
      totalTtfMs: this.span("boot_start", "first_frame"),
      spans,
      manifestUrl: manifest?.url,
      manifestMs: manifest?.durationMs,
      firstSegmentUrl: firstSegment?.url,
      firstSegmentMs: firstSegment?.durationMs,
      resources,
    };
  }

  private connectionType(): string | undefined {
    const conn = (navigator as unknown as { connection?: { effectiveType?: string } } | undefined)
      ?.connection;
    return conn?.effectiveType;
  }

  private collectResources(): BootResourceTiming[] {
    if (typeof performance === "undefined" || typeof performance.getEntriesByType !== "function") {
      return [];
    }
    const bootStart = this.marks.get("boot_start") ?? 0;
    const hosts = this.endpointHosts();

    const entries = performance.getEntriesByType("resource") as PerformanceResourceTiming[];
    const out: BootResourceTiming[] = [];
    for (const entry of entries) {
      if (entry.startTime < bootStart) continue;
      const kind = classifyResource(entry.name);
      // Keep media-relevant kinds, or anything served by a resolved endpoint host.
      const relevant = kind !== "other" || hosts.some((h) => entry.name.includes(h));
      if (!relevant) continue;

      const owned = this.ownedResponses.get(entry.name);
      out.push({
        kind,
        url: redactUrl(entry.name),
        ttfbMs: Math.max(0, Math.round(entry.responseStart - entry.requestStart)),
        durationMs: Math.round(entry.duration),
        transferSize: entry.transferSize ?? 0,
        encodedBodySize: entry.encodedBodySize ?? 0,
        decodedBodySize: entry.decodedBodySize ?? 0,
        cacheStatus: owned?.cacheStatus,
        ageSeconds: owned?.ageSeconds,
        serverTiming: this.serverTiming(entry),
      });
      if (out.length >= 32) break;
    }
    return out;
  }

  private serverTiming(
    entry: PerformanceResourceTiming
  ): Array<{ name: string; duration?: number; description?: string }> | undefined {
    const st = (entry as unknown as { serverTiming?: PerformanceServerTiming[] }).serverTiming;
    if (!st || st.length === 0) return undefined;
    return st.map((s) => ({ name: s.name, duration: s.duration, description: s.description }));
  }

  private endpointHosts(): string[] {
    const endpoints = this.config.getEndpoints?.() ?? null;
    if (!endpoints) return [];
    const urls = [endpoints.primary, ...(endpoints.fallbacks ?? [])]
      .flatMap((e) => [e?.url, e?.baseUrl])
      .filter((u): u is string => typeof u === "string" && u.length > 0);
    const hosts = new Set<string>();
    for (const u of urls) {
      try {
        hosts.add(new URL(u).host);
      } catch {
        // ignore unparseable urls
      }
    }
    return Array.from(hosts);
  }
}

export default BootTracer;
