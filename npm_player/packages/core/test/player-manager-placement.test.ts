import { describe, expect, it, vi } from "vitest";
import { PlayerManager } from "../src/core/PlayerManager";
import type { IPlayer, StreamInfo } from "../src/core/PlayerInterface";

function info(
  url = "https://selected.example/live.m3u8",
  type = "html5/application/vnd.apple.mpegurl"
): StreamInfo {
  return {
    source: [{ url, type }],
    meta: { tracks: [{ type: "video", codec: "H264" }] },
    type: "live",
  };
}

function player(name: string, priority = 1): IPlayer {
  return {
    capability: { name, shortname: name, priority, mimes: ["html5/application/vnd.apple.mpegurl"] },
    isMimeSupported: vi.fn((type) => type === "html5/application/vnd.apple.mpegurl"),
    isBrowserSupported: vi.fn(() => ["video"]),
    initialize: vi.fn().mockResolvedValue({}),
    destroy: vi.fn(),
  } as unknown as IPlayer;
}

describe("placement-backed player fallback", () => {
  it("never invents an embedded-player source, including when forced", async () => {
    const manager = new PlayerManager();
    const hls = player("hls");
    const legacy = player("mist-legacy", 99);
    legacy.capability.mimes = ["mist/legacy"];
    legacy.isMimeSupported = vi.fn((type) => type === "mist/legacy");
    manager.registerPlayer(hls);
    manager.registerPlayer(legacy);
    const selected = info();
    expect(
      manager.getAllCombinations(selected).every((combo) => selected.source.includes(combo.source))
    ).toBe(true);
    expect(manager.selectBestPlayer(selected, { forcePlayer: "mist-legacy" })).toMatchObject({
      player: "hls",
      source: selected.source[0],
    });
    const replacement = info("https://authorized.example/replacement.m3u8");
    const resolveFallback = vi.fn().mockResolvedValue(replacement);
    await manager.initializePlayer(
      { innerHTML: "" } as HTMLElement,
      selected,
      {},
      { resolveFallback }
    );
    expect(await manager.tryPlaybackFallback()).toBe(true);
    expect(resolveFallback).toHaveBeenCalledOnce();
    expect(legacy.initialize).not.toHaveBeenCalled();
    expect(hls.initialize).toHaveBeenLastCalledWith(
      expect.anything(),
      replacement.source[0],
      {},
      replacement
    );
  });

  it("keeps transient media retry on the same prepared URL without calling placement", async () => {
    const manager = new PlayerManager();
    const hls = player("hls");
    vi.mocked(hls.initialize).mockRejectedValueOnce(new Error("codec decode error"));
    manager.registerPlayer(hls);
    const resolveFallback = vi.fn();
    await manager.initializePlayer(
      { innerHTML: "" } as HTMLElement,
      info(),
      {},
      { resolveFallback }
    );
    expect(hls.initialize).toHaveBeenCalledTimes(2);
    expect(resolveFallback).not.toHaveBeenCalled();
  });

  it("re-resolves after a recoverable initialization failure exhausts the selected format", async () => {
    const manager = new PlayerManager();
    const hls = player("hls");
    vi.mocked(hls.initialize).mockRejectedValueOnce(new Error("protocol unsupported"));
    manager.registerPlayer(hls);
    const replacement = info("https://new-prepared.example/live.m3u8");
    const resolveFallback = vi.fn().mockResolvedValue(replacement);
    await manager.initializePlayer(
      { innerHTML: "" } as HTMLElement,
      info(),
      {},
      { resolveFallback }
    );
    expect(resolveFallback).toHaveBeenCalledOnce();
    expect(hls.initialize).toHaveBeenCalledTimes(2);
    expect(hls.initialize).toHaveBeenLastCalledWith(
      expect.anything(),
      replacement.source[0],
      {},
      replacement
    );
  });

  it("tries another player on the selected URL before requesting another destination", async () => {
    const manager = new PlayerManager();
    const first = player("first");
    const second = player("second", 2);
    manager.registerPlayer(first);
    manager.registerPlayer(second);
    const resolver = vi.fn();
    await manager.initializePlayer(
      { innerHTML: "" } as HTMLElement,
      info(),
      {},
      { resolveFallback: resolver }
    );
    const original = manager.getCurrentPlayer();
    expect(await manager.tryPlaybackFallback()).toBe(true);
    expect(manager.getCurrentPlayer()).not.toBe(original);
    expect(resolver).not.toHaveBeenCalled();
  });

  it("requests a prepared source when the initial format has no compatible player", async () => {
    const manager = new PlayerManager();
    const hls = player("hls");
    manager.registerPlayer(hls);
    const selected = info();
    const resolveFallback = vi.fn().mockResolvedValue(selected);
    await manager.initializePlayer(
      { innerHTML: "" } as HTMLElement,
      info("wss://selected.example/webrtc", "mist/webrtc"),
      {},
      { resolveFallback }
    );
    expect(resolveFallback).toHaveBeenCalledOnce();
    expect(hls.initialize).toHaveBeenCalledWith(
      expect.anything(),
      selected.source[0],
      {},
      selected
    );
  });

  it("uses a fresh destination for runtime fallback and stores it for subsequent retries", async () => {
    const manager = new PlayerManager();
    const hls = player("hls");
    manager.registerPlayer(hls);
    const replacement = info("https://another-authorized.example/live.m3u8");
    const resolveFallback = vi.fn().mockResolvedValue(replacement);
    await manager.initializePlayer(
      { innerHTML: "" } as HTMLElement,
      info(),
      {},
      { resolveFallback }
    );
    expect(await manager.tryPlaybackFallback()).toBe(true);
    expect(hls.initialize).toHaveBeenLastCalledWith(
      expect.anything(),
      replacement.source[0],
      {},
      replacement
    );
    expect(await manager.tryPlaybackFallback()).toBe(false);
    expect(resolveFallback).toHaveBeenLastCalledWith(replacement);
  });

  it("bounds repeated fresh resolutions even when returned formats remain unplayable", async () => {
    const manager = new PlayerManager();
    manager.registerPlayer(player("hls"));
    let attempt = 0;
    const resolveFallback = vi.fn(async () =>
      info(`https://authorized.example/${++attempt}`, "unsupported")
    );
    await expect(
      manager.initializePlayer(
        { innerHTML: "" } as HTMLElement,
        info("https://authorized.example/initial", "unsupported"),
        {},
        { resolveFallback }
      )
    ).rejects.toThrow();
    expect(resolveFallback).toHaveBeenCalledTimes(3);
  });

  it("stops on authority failure without restoring the previous source", async () => {
    const manager = new PlayerManager();
    const hls = player("hls");
    manager.registerPlayer(hls);
    const resolveFallback = vi.fn().mockRejectedValue(new Error("placement denied"));
    await manager.initializePlayer(
      { innerHTML: "" } as HTMLElement,
      info(),
      {},
      { resolveFallback }
    );
    expect(await manager.tryPlaybackFallback()).toBe(false);
    expect(hls.initialize).toHaveBeenCalledTimes(1);
    expect(resolveFallback).toHaveBeenCalledOnce();
  });
});
