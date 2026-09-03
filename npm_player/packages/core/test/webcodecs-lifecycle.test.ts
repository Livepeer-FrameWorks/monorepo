import { afterEach, describe, expect, it, vi } from "vitest";

import { isIPadWithBrokenHEVC } from "../src/core/detector";
import { WebCodecsPlayerImpl, canUseWebGLVideoFrameTextures } from "../src/players/WebCodecsPlayer";

describe("WebCodecs lifecycle", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("ignores stale play and pause calls after teardown", async () => {
    const player = new WebCodecsPlayerImpl();
    await player.destroy();

    await expect(player.play()).resolves.toBeUndefined();
    expect(() => player.pause()).not.toThrow();
  });

  it("disables VideoFrame WebGL textures on every iOS browser", () => {
    vi.stubGlobal("window", {});
    vi.stubGlobal("navigator", {
      userAgent:
        "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 CriOS/128.0 Mobile/15E148 Safari/604.1",
    });

    expect(canUseWebGLVideoFrameTextures()).toBe(false);
  });

  it("disables VideoFrame WebGL textures on desktop-UA iPadOS Safari", () => {
    vi.stubGlobal("window", {});
    vi.stubGlobal("navigator", {
      userAgent:
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15",
      maxTouchPoints: 5,
    });

    expect(canUseWebGLVideoFrameTextures()).toBe(false);
  });

  it("keeps VideoFrame WebGL textures available off iOS", () => {
    vi.stubGlobal("window", {});
    vi.stubGlobal("navigator", {
      userAgent:
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_6) AppleWebKit/605.1.15 Version/17.6 Safari/605.1.15",
      maxTouchPoints: 0,
    });

    expect(canUseWebGLVideoFrameTextures()).toBe(true);
  });

  it("uses the Safari version for desktop-UA iPad HEVC compatibility", () => {
    vi.stubGlobal("navigator", {
      userAgent:
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 Version/16.6 Safari/605.1.15",
      maxTouchPoints: 5,
    });
    expect(isIPadWithBrokenHEVC()).toBe(true);

    vi.stubGlobal("navigator", {
      userAgent:
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15",
      maxTouchPoints: 5,
    });
    expect(isIPadWithBrokenHEVC()).toBe(false);
  });

  it("reissues the codec handshake and desired tracks on every socket connection", () => {
    const player = new WebCodecsPlayerImpl();
    const listeners: Record<string, (value: any) => void> = {};
    const requestCodecData = vi.fn();
    const setTracks = vi.fn();
    (player as any).wsController = {
      on: (event: string, callback: (value: any) => void) => {
        listeners[event] = callback;
      },
      requestCodecData,
      setTracks,
    };
    (player as any).supportedCombinations = [[["AAC"], ["H264"]]];
    (player as any).mediaTrackControl = "video";
    (player as any).selectedVideoTrack = "4";
    (player as any).videoSelectionExplicit = true;
    (player as any).selectedAudioTrackId = "2";
    (player as any).setupWebSocketHandlers();

    listeners.statechange("connected");
    listeners.statechange("reconnecting");
    listeners.statechange("connected");

    expect(requestCodecData).toHaveBeenCalledTimes(2);
    expect(setTracks).toHaveBeenCalledTimes(2);
    expect(setTracks).toHaveBeenLastCalledWith({ video: "4" });
  });

  it("opens metadata transport when a subscription arrives after metadata-free load", () => {
    const player = new WebCodecsPlayerImpl();
    (player as any).metadataSourceUrl = "wss://mist.test/raw/live";
    (player as any).isDestroyed = false;
    const initMetadata = vi
      .spyOn(player as any, "initMetadataWebSocket")
      .mockImplementation(() => {});

    player.subscribeToMetaTrack("7", vi.fn(), "subtitle");

    expect(initMetadata).toHaveBeenCalledWith("wss://mist.test/raw/live");
  });

  it("emits a track-list change when late WebCodecs info arrives", async () => {
    const player = new WebCodecsPlayerImpl();
    const changed = vi.fn();
    player.on("trackschange", changed);

    await (player as any).handleInfo({
      meta: { tracks: { "7": { idx: 7, type: "meta", codec: "subtitle", lang: "en" } } },
    });

    expect(changed).toHaveBeenCalledTimes(1);
    expect(player.getTextTracks()).toEqual([expect.objectContaining({ id: "7", lang: "en" })]);
  });

  it("treats a Mist tracks message as selected numeric ids without erasing metadata", async () => {
    const player = new WebCodecsPlayerImpl();
    const video = { idx: 1, type: "video", codec: "H264" };
    const audio = { idx: 2, type: "audio", codec: "AAC", lang: "en" };
    const subtitle = { idx: 7, type: "meta", codec: "subtitle", lang: "nl" };
    (player as any).tracksByIndex = new Map([
      [1, video],
      [2, audio],
      [7, subtitle],
    ]);
    (player as any).pipelines = new Map([[1, {}]]);

    await (player as any).handleTracksChange({
      type: "tracks",
      tracks: [1],
      codecs: ["avc1.42e01f"],
    });

    expect(Array.from((player as any).tracksByIndex.keys())).toEqual([1, 2, 7]);
    expect((player as any).selectedMediaTrackIds).toEqual(new Set([1]));
    expect((player as any).tracksByIndex.get(2)).toEqual(expect.objectContaining({ lang: "en" }));
    expect(player.getAudioTracks()).toEqual([]);
    expect(player.getTextTracks()).toEqual([expect.objectContaining({ id: "7", lang: "nl" })]);
  });
});
