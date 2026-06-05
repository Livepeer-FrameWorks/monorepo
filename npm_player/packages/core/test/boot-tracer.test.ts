import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BootTracer, type BootTrace } from "../src/core/BootTracer";

/** Drives `performance.now()` deterministically so span math is assertable. */
function useFakeClock() {
  let t = 0;
  const spy = vi.spyOn(performance, "now").mockImplementation(() => t);
  return {
    set: (v: number) => {
      t = v;
    },
    restore: () => spy.mockRestore(),
  };
}

function makeVideoWithRvfc() {
  let cb: (() => void) | null = null;
  return {
    el: {
      requestVideoFrameCallback: (fn: () => void) => {
        cb = fn;
        return 1;
      },
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    } as unknown as HTMLVideoElement,
    fireFirstFrame: () => cb?.(),
  };
}

function makeVideoWithEvents() {
  const handlers = new Map<string, () => void>();
  return {
    el: {
      addEventListener: (name: string, fn: () => void) => handlers.set(name, fn),
      removeEventListener: (name: string) => handlers.delete(name),
    } as unknown as HTMLVideoElement,
    fire: (name: string) => handlers.get(name)?.(),
  };
}

describe("BootTracer", () => {
  let clock: ReturnType<typeof useFakeClock>;

  beforeEach(() => {
    clock = useFakeClock();
  });

  afterEach(() => {
    clock.restore();
    vi.restoreAllMocks();
  });

  it("computes the boot waterfall spans and total TTF", () => {
    clock.set(0);
    let trace: BootTrace | null = null;
    const tracer = new BootTracer({
      contentId: "demo",
      contentType: "live",
      onComplete: (t) => {
        trace = t;
      },
    });

    clock.set(10);
    tracer.onState("gateway_loading");
    clock.set(40);
    tracer.mark("gateway_resolved");
    clock.set(60);
    tracer.onState("gateway_ready");
    clock.set(70);
    tracer.onState("selecting_player");
    clock.set(90);
    tracer.onState("connecting", {
      selectedPlayer: "hlsjs",
      selectedProtocol: "html5/application/vnd.apple.mpegurl",
    });
    clock.set(130);
    tracer.onState("buffering");
    clock.set(200);

    const video = makeVideoWithRvfc();
    tracer.attachVideo(video.el);
    video.fireFirstFrame();

    expect(trace).not.toBeNull();
    const t = trace as unknown as BootTrace;
    expect(t.outcome).toBe("success");
    expect(t.totalTtfMs).toBe(200);
    expect(t.spans.gatewayResolveMs).toBe(30);
    expect(t.spans.mistHydrateMs).toBe(20);
    expect(t.spans.playerSelectMs).toBe(20);
    expect(t.spans.connectMs).toBe(40);
    expect(t.spans.prebufferMs).toBe(70);
    expect(t.playerType).toBe("hlsjs");
    expect(t.protocol).toBe("html5/application/vnd.apple.mpegurl");
  });

  it("falls back to playing/canplay when requestVideoFrameCallback is unavailable", () => {
    clock.set(0);
    let trace: BootTrace | null = null;
    const tracer = new BootTracer({ contentId: "demo", onComplete: (t) => (trace = t) });

    const video = makeVideoWithEvents();
    clock.set(150);
    tracer.attachVideo(video.el);
    video.fire("playing");

    expect((trace as unknown as BootTrace).outcome).toBe("success");
    expect((trace as unknown as BootTrace).totalTtfMs).toBe(150);
  });

  it("reports abandoned when torn down before first frame", () => {
    let trace: BootTrace | null = null;
    const tracer = new BootTracer({ contentId: "demo", onComplete: (t) => (trace = t) });
    tracer.onState("gateway_loading");
    tracer.abandon();

    expect((trace as unknown as BootTrace).outcome).toBe("abandoned");
    expect((trace as unknown as BootTrace).totalTtfMs).toBeUndefined();
  });

  it("reports error when an error was recorded before teardown", () => {
    let trace: BootTrace | null = null;
    const tracer = new BootTracer({ contentId: "demo", onComplete: (t) => (trace = t) });
    tracer.onState("gateway_error", { error: "resolve failed" });
    tracer.abandon();

    expect((trace as unknown as BootTrace).outcome).toBe("error");
    expect((trace as unknown as BootTrace).errorCode).toBe("resolve failed");
  });

  it("cancel() drops the trace without emitting, even if the frame callback fires later", () => {
    const onComplete = vi.fn();
    const tracer = new BootTracer({ contentId: "demo", onComplete });
    const video = makeVideoWithRvfc();
    tracer.attachVideo(video.el);

    tracer.cancel();
    // A superseded tracer's orphaned requestVideoFrameCallback must not emit.
    video.fireFirstFrame();
    tracer.abandon();

    expect(onComplete).not.toHaveBeenCalled();
    expect(tracer.isCompleted()).toBe(true);
  });

  it("finalizes exactly once", () => {
    const onComplete = vi.fn();
    const tracer = new BootTracer({ contentId: "demo", onComplete });
    tracer.finalize("success");
    tracer.finalize("error");
    tracer.abandon();
    expect(onComplete).toHaveBeenCalledTimes(1);
  });

  it("matches Resource Timing entries and merges player-owned cache headers", () => {
    clock.set(0);
    const entries: Partial<PerformanceResourceTiming>[] = [
      {
        name: "https://edge.example.com/json_demo.js?metaeverywhere=1",
        startTime: 5,
        requestStart: 5,
        responseStart: 15,
        duration: 25,
        transferSize: 1200,
        encodedBodySize: 1000,
        decodedBodySize: 3000,
      },
      {
        name: "https://edge.example.com/hls/demo/index.m3u8?jwt=secret-token&t=123#frag",
        startTime: 30,
        requestStart: 30,
        responseStart: 38,
        duration: 18,
        transferSize: 800,
        encodedBodySize: 700,
        decodedBodySize: 700,
      },
      {
        name: "https://unrelated.cdn.net/analytics.js",
        startTime: 2,
        requestStart: 2,
        responseStart: 4,
        duration: 9,
        transferSize: 100,
        encodedBodySize: 90,
        decodedBodySize: 90,
      },
    ];
    vi.spyOn(performance, "getEntriesByType").mockReturnValue(
      entries as PerformanceResourceTiming[]
    );

    let trace: BootTrace | null = null;
    const tracer = new BootTracer({
      contentId: "demo",
      getEndpoints: () =>
        ({
          primary: {
            nodeId: "n1",
            protocol: "HLS",
            url: "https://edge.example.com/hls/demo/index.m3u8",
          },
          fallbacks: [],
        }) as never,
      onComplete: (t) => (trace = t),
    });

    tracer.recordOwnedResponse(
      "https://edge.example.com/json_demo.js?metaeverywhere=1",
      new Headers({ "x-cache": "HIT", age: "42" })
    );

    const video = makeVideoWithRvfc();
    clock.set(120);
    tracer.attachVideo(video.el);
    video.fireFirstFrame();

    const t = trace as unknown as BootTrace;
    const kinds = t.resources.map((r) => r.kind).sort();
    // mist_json + manifest matched; the unrelated host is excluded.
    expect(kinds).toEqual(["manifest", "mist_json"]);
    const mist = t.resources.find((r) => r.kind === "mist_json")!;
    expect(mist.ttfbMs).toBe(10);
    expect(mist.cacheStatus).toBe("HIT");
    expect(mist.ageSeconds).toBe(42);
    // Query string + fragment (incl. the signed jwt) must be stripped before storage.
    expect(t.manifestUrl).toBe("https://edge.example.com/hls/demo/index.m3u8");
    expect(t.resources.find((r) => r.kind === "manifest")!.url).not.toContain("jwt");
    expect(t.manifestMs).toBe(18);
  });
});
