import { describe, expect, it, vi } from "vitest";
import { PlayerController } from "../src/core/PlayerController";
import type { LoadingPosterInfo } from "../src/types";

function makeController(opts: { poster?: string } = {}): PlayerController {
  return new PlayerController({
    contentId: "test-stream",
    contentType: "live",
    poster: opts.poster,
    playerManager: {
      on: vi.fn(() => () => {}),
    } as any,
  });
}

function setAssets(c: PlayerController, assets: any) {
  (c as any)._thumbnailAssets = assets;
}
function setPreviewUrl(c: PlayerController, url: string | null) {
  (c as any)._previewUrl = url;
}
function setRawCues(c: PlayerController, cues: any[]) {
  (c as any)._rawThumbnailCues = cues;
}
function emitPoster(c: PlayerController, force = false) {
  (c as any).emitLoadingPosterChange(force);
}
function lastPoster(c: PlayerController): LoadingPosterInfo | null {
  return (c as any)._lastLoadingPoster;
}

describe("PlayerController.buildLoadingPosterInfo", () => {
  it("returns null when nothing is available", () => {
    const c = makeController();
    expect(c.getLoadingPoster()).toBeNull();
  });

  it("static / chandler-poster when only Chandler poster present", () => {
    const c = makeController();
    setAssets(c, {
      posterUrl: "https://chandler/p.jpg",
      assetKey: "k",
    });
    const p = c.getLoadingPoster()!;
    expect(p.mode).toBe("static");
    expect(p.staticUrl).toBe("https://chandler/p.jpg");
    expect(p.staticSource).toBe("chandler-poster");
    expect(p.spriteJpgUrl).toBeUndefined();
    expect(p.geometry).toBeUndefined();
  });

  it("static / mist-preview when only mist preview present", () => {
    const c = makeController();
    setPreviewUrl(c, "https://mist/pre.jpg");
    const p = c.getLoadingPoster()!;
    expect(p.mode).toBe("static");
    expect(p.staticSource).toBe("mist-preview");
  });

  it("static / thumbnail-prop when only the wrapper thumbnailUrl present", () => {
    const c = makeController({ poster: "https://user/thumb.jpg" });
    const p = c.getLoadingPoster()!;
    expect(p.mode).toBe("static");
    expect(p.staticSource).toBe("thumbnail-prop");
    expect(p.staticUrl).toBe("https://user/thumb.jpg");
  });

  it("animate / synthetic when sprite URL is known but no cues", () => {
    const c = makeController();
    setAssets(c, {
      spriteJpgUrl: "https://chandler/s.jpg",
      assetKey: "k",
    });
    const p = c.getLoadingPoster()!;
    expect(p.mode).toBe("animate");
    expect(p.geometry).toBe("synthetic");
    expect(p.spriteJpgUrl).toBe("https://chandler/s.jpg");
    expect(p.cues).toHaveLength(0);
    expect(p.columns).toBe(0);
    expect(p.rows).toBe(0);
    expect(p.tileWidth).toBe(0);
    expect(p.tileHeight).toBe(0);
  });

  it("animate / measured when real VTT cues are present", () => {
    const c = makeController();
    setAssets(c, { spriteJpgUrl: "https://c/s.jpg", assetKey: "k" });
    const cues = [
      { url: "https://c/s.jpg", x: 0, y: 0, width: 160, height: 90, startTime: 0, endTime: 1 },
      { url: "https://c/s.jpg", x: 160, y: 0, width: 160, height: 90, startTime: 1, endTime: 2 },
      { url: "https://c/s.jpg", x: 0, y: 90, width: 160, height: 90, startTime: 2, endTime: 3 },
    ];
    setRawCues(c, cues);
    const p = c.getLoadingPoster()!;
    expect(p.mode).toBe("animate");
    expect(p.geometry).toBe("measured");
    expect(p.cues).toHaveLength(3);
    expect(p.tileWidth).toBe(160);
    expect(p.tileHeight).toBe(90);
    expect(p.columns).toBe(2);
    expect(p.rows).toBe(2);
    expect(p.spriteWidth).toBe(320);
    expect(p.spriteHeight).toBe(180);
  });

  it("prefers Chandler poster over mist preview over thumbnailUrl", () => {
    const c = makeController({ poster: "https://user/thumb.jpg" });
    setPreviewUrl(c, "https://mist/pre.jpg");
    setAssets(c, { posterUrl: "https://chandler/p.jpg", assetKey: "k" });
    const p = c.getLoadingPoster()!;
    expect(p.staticSource).toBe("chandler-poster");
    expect(p.staticUrl).toBe("https://chandler/p.jpg");
  });

  it("includes staticUrl as fallback even in animate mode", () => {
    const c = makeController();
    setAssets(c, {
      posterUrl: "https://c/p.jpg",
      spriteJpgUrl: "https://c/s.jpg",
      assetKey: "k",
    });
    const p = c.getLoadingPoster()!;
    expect(p.mode).toBe("animate");
    expect(p.staticUrl).toBe("https://c/p.jpg");
    expect(p.staticSource).toBe("chandler-poster");
  });

  it("uses cue-bound URL (live blob) when present, else falls back to assets URL", () => {
    const c = makeController();
    setAssets(c, { spriteJpgUrl: "https://chandler/s.jpg", assetKey: "k" });
    setRawCues(c, [
      { url: "blob:abc", x: 0, y: 0, width: 160, height: 90, startTime: 0, endTime: 1 },
      { url: "blob:abc", x: 160, y: 0, width: 160, height: 90, startTime: 1, endTime: 2 },
    ]);
    const p = c.getLoadingPoster()!;
    expect(p.spriteJpgUrl).toBe("blob:abc");
  });
});

describe("PlayerController.emitLoadingPosterChange (generation/dedup)", () => {
  it("does not emit or bump generation when nothing changed", () => {
    const c = makeController();
    setAssets(c, { posterUrl: "https://c/p.jpg", assetKey: "k" });
    const events: any[] = [];
    c.on("loadingPosterChange", (e) => events.push(e));

    emitPoster(c);
    expect(events).toHaveLength(1);
    const gen1 = lastPoster(c)!.generation;

    emitPoster(c); // same inputs
    expect(events).toHaveLength(1);
    expect(lastPoster(c)!.generation).toBe(gen1);
  });

  it("bumps generation on URL change", () => {
    const c = makeController();
    setAssets(c, { posterUrl: "https://c/p.jpg", assetKey: "k" });
    emitPoster(c);
    const gen1 = lastPoster(c)!.generation;

    setAssets(c, { posterUrl: "https://c/p2.jpg", assetKey: "k2" });
    emitPoster(c);
    expect(lastPoster(c)!.generation).toBeGreaterThan(gen1);
  });

  it("bumps generation on geometry transition synthetic -> measured", () => {
    const c = makeController();
    setAssets(c, { spriteJpgUrl: "https://c/s.jpg", assetKey: "k" });
    emitPoster(c);
    const synthetic = lastPoster(c)!;
    expect(synthetic.geometry).toBe("synthetic");

    setRawCues(c, [
      { url: "https://c/s.jpg", x: 0, y: 0, width: 160, height: 90, startTime: 0, endTime: 1 },
      { url: "https://c/s.jpg", x: 160, y: 0, width: 160, height: 90, startTime: 1, endTime: 2 },
    ]);
    emitPoster(c);
    const measured = lastPoster(c)!;
    expect(measured.geometry).toBe("measured");
    expect(measured.generation).toBeGreaterThan(synthetic.generation);
  });

  it("bumps generation when forceBump=true even if fields unchanged (asset refresh)", () => {
    const c = makeController();
    setAssets(c, { posterUrl: "https://c/p.jpg", assetKey: "k" });
    emitPoster(c);
    const gen1 = lastPoster(c)!.generation;

    emitPoster(c, true);
    expect(lastPoster(c)!.generation).toBeGreaterThan(gen1);
  });
});

describe("PlayerController.getShouldShowLoadingPoster", () => {
  it("false when no spec available", () => {
    const c = makeController();
    expect(c.getShouldShowLoadingPoster()).toBe(false);
  });

  it("true when spec set, playback not started, no error, status idle/cold", () => {
    const c = makeController();
    setAssets(c, { posterUrl: "https://c/p.jpg", assetKey: "k" });
    emitPoster(c);
    (c as any).endpoints = { primary: { url: "https://e" } };
    (c as any).streamState = { isOnline: true, status: "ONLINE" };
    expect(c.getShouldShowLoadingPoster()).toBe(true);
  });

  it("true while waiting for endpoint so poster beats idle during player swaps", () => {
    const c = makeController();
    setAssets(c, { posterUrl: "https://c/p.jpg", assetKey: "k" });
    emitPoster(c);
    (c as any).endpoints = null;
    (c as any).state = "gateway_loading";

    expect(c.shouldShowWaitingForEndpoint()).toBe(true);
    expect(c.getShouldShowLoadingPoster()).toBe(true);
  });

  it("false once playback has started", () => {
    const c = makeController();
    setAssets(c, { posterUrl: "https://c/p.jpg", assetKey: "k" });
    emitPoster(c);
    (c as any).endpoints = { primary: { url: "https://e" } };
    (c as any).streamState = { isOnline: true, status: "ONLINE" };
    (c as any)._hasPlaybackStarted = true;
    expect(c.getShouldShowLoadingPoster()).toBe(false);
  });

  it("false when stream status is OFFLINE/ERROR/INVALID", () => {
    for (const status of ["OFFLINE", "ERROR", "INVALID"]) {
      const c = makeController();
      setAssets(c, { posterUrl: "https://c/p.jpg", assetKey: "k" });
      emitPoster(c);
      (c as any).endpoints = { primary: { url: "https://e" } };
      (c as any).streamState = { isOnline: false, status };
      expect(c.getShouldShowLoadingPoster()).toBe(false);
    }
  });

  it("false when an error is set; true again after errorCleared", () => {
    const c = makeController();
    setAssets(c, { posterUrl: "https://c/p.jpg", assetKey: "k" });
    emitPoster(c);
    (c as any).endpoints = { primary: { url: "https://e" } };
    (c as any).streamState = { isOnline: true, status: "ONLINE" };
    (c as any)._errorText = "boom";
    expect(c.getShouldShowLoadingPoster()).toBe(false);
    (c as any)._errorText = null;
    expect(c.getShouldShowLoadingPoster()).toBe(true);
  });

  it("regression: flipping true the moment loadingPosterChange fires (no stateChange needed)", () => {
    // The previous design tied recompute to stateChange. This regression guard models
    // a wrapper that subscribes to loadingPosterChange and recomputes — must see the flip.
    const c = makeController();
    (c as any).endpoints = { primary: { url: "https://e" } };
    (c as any).streamState = { isOnline: true, status: "ONLINE" };
    expect(c.getShouldShowLoadingPoster()).toBe(false);

    let snapshot: boolean | null = null;
    c.on("loadingPosterChange", () => {
      snapshot = c.getShouldShowLoadingPoster();
    });
    setAssets(c, { posterUrl: "https://c/p.jpg", assetKey: "k" });
    emitPoster(c);
    expect(snapshot).toBe(true);
  });
});
