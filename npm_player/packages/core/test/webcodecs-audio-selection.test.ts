import { describe, expect, it, vi } from "vitest";

import { buildSupportedAudioCodecList, WebCodecsPlayerImpl } from "../src/players/WebCodecsPlayer";
import { MetadataWebSocket } from "../src/players/WebCodecsPlayer/MetadataWebSocket";
import { SyncController } from "../src/players/WebCodecsPlayer/SyncController";

describe("WebCodecs audio stability", () => {
  it("advertises all supported raw WebCodecs audio codecs", () => {
    const codecs = buildSupportedAudioCodecList([
      { codec: "AAC", rate: 44100 },
      { codec: "opus", rate: 48000 },
      { codec: "AAC", rate: 44100 },
    ]);

    expect(codecs).toEqual(["AAC", "opus"]);
  });

  it("keeps local rate tweaks enabled by default", () => {
    const speedChanges: number[] = [];
    const sync = new SyncController({
      isLive: true,
      onSpeedChange: (_main, tweak) => speedChanges.push(tweak),
    });

    const desired = sync.getDesiredBuffer();
    sync.evaluateBuffer(desired * 3, {
      playRateCurr: "auto",
      serverCurrentMs: 1_000,
      serverEndMs: 2_000,
      serverJitterMs: 100,
    });

    expect(speedChanges).toEqual([1.05]);
  });

  it("does not advertise or fake audio selection on the raw WebSocket output", () => {
    const player = new WebCodecsPlayerImpl();
    const setTracks = vi.fn();
    (player as any).wsController = { setTracks };
    (player as any).tracksByIndex = new Map([
      [2, { type: "audio", codec: "AAC", idx: 2, lang: "en" }],
      [3, { type: "audio", codec: "AAC", idx: 3, lang: "es" }],
    ]);

    player.selectAudioTrack("3");
    expect(setTracks).not.toHaveBeenCalled();
    expect(player.getAudioTracks()).toEqual([]);
  });

  it("does not advertise or fake quality selection on the raw WebSocket output", () => {
    const player = new WebCodecsPlayerImpl();
    const setTracks = vi.fn();
    (player as any).wsController = { setTracks };
    (player as any).tracksByIndex = new Map([
      [1, { type: "video", codec: "H264", idx: 1, width: 640, height: 360 }],
      [4, { type: "video", codec: "H264", idx: 4, width: 1920, height: 1080 }],
    ]);

    player.selectQuality("4");
    (player as any).replayDesiredMediaTracks();

    expect(setTracks).not.toHaveBeenCalled();
    expect(player.getQualities()).toEqual([expect.objectContaining({ id: "auto", active: true })]);
  });

  it("replays a desired video selection on the H264 output", () => {
    const player = new WebCodecsPlayerImpl();
    (player as any).mediaTrackControl = "video";
    player.selectQuality("4");
    const setTracks = vi.fn();
    (player as any).wsController = { setTracks };

    (player as any).replayDesiredMediaTracks();

    expect(setTracks).toHaveBeenCalledWith({ video: "4" });
  });

  it("resets automatic H264 video selection by removing the server selector", () => {
    const player = new WebCodecsPlayerImpl();
    (player as any).mediaTrackControl = "video";
    player.selectQuality("4");
    player.selectQuality("auto");
    const setTracks = vi.fn();
    (player as any).wsController = { setTracks };

    (player as any).replayDesiredMediaTracks();

    expect(setTracks).toHaveBeenCalledWith({ video: null });
  });

  it("preserves plain Mist subtitle payloads and exposes transport duration separately", () => {
    const socket = new MetadataWebSocket(
      "wss://mist.test/output.js",
      () => 0,
      () => 1,
      () => false,
      { resolveEventType: () => "subtitle" }
    );
    const event = (socket as any).toMetaTrackEvent({
      time: 1_000,
      track: "7",
      data: "true",
      duration: 2_000,
    });
    expect(event).toMatchObject({
      type: "subtitle",
      timestamp: 1_000,
      trackId: "7",
      data: "true",
      durationMs: 2_000,
    });
  });

  it("uses the subtitle subscription type before track metadata is available", () => {
    const socket = new MetadataWebSocket(
      "wss://mist.test/output.js",
      () => 0,
      () => 1,
      () => false
    );
    (socket as any).transport = {};
    socket.subscribe("7", vi.fn(), "subtitle");
    const event = (socket as any).toMetaTrackEvent({
      time: 1_000,
      track: "7",
      data: "plain cue",
    });
    expect(event).toMatchObject({ type: "subtitle", data: "plain cue" });
  });

  it("preserves the two-argument subtitle subscription contract for plain cues", () => {
    const socket = new MetadataWebSocket(
      "wss://mist.test/output.js",
      () => 0,
      () => 1,
      () => false
    );
    (socket as any).transport = {};
    socket.subscribe("7", vi.fn());
    const event = (socket as any).toMetaTrackEvent({
      time: 1_000,
      track: "7",
      data: "plain cue",
    });
    expect(event.type).toBe("subtitle");
  });

  it("preserves the compatibility type and sends all for all-track subscriptions", () => {
    const socket = new MetadataWebSocket(
      "wss://mist.test/output.js",
      () => 0,
      () => 1,
      () => false
    );
    const send = vi.fn();
    (socket as any).send = send;
    (socket as any).transport = {};
    socket.subscribe("all", vi.fn());
    (socket as any).sendTrackSelection();
    const event = (socket as any).toMetaTrackEvent({
      time: 1_000,
      track: "7",
      data: "plain cue",
    });

    expect(send).toHaveBeenCalledWith({ type: "tracks", meta: "all" });
    expect(event.type).toBe("subtitle");
  });

  it("keeps the pending unsubscriber live after the metadata socket is created", () => {
    const player = new WebCodecsPlayerImpl();
    const activeUnsubscribe = vi.fn();
    const subscribe = vi.fn(() => activeUnsubscribe);
    const unsubscribe = player.subscribeToMetaTrack("7", vi.fn(), "subtitle");

    (player as any).metadataWs = { subscribe };
    (player as any).activatePendingMetaSubscriptions();
    unsubscribe();

    expect(subscribe).toHaveBeenCalledOnce();
    expect(activeUnsubscribe).toHaveBeenCalledOnce();
  });

  it("infers generic metadata type without changing the subscriber payload", () => {
    const socket = new MetadataWebSocket(
      "wss://mist.test/output.js",
      () => 0,
      () => 1,
      () => false
    );
    const data = '{"name":"goal"}';
    const event = (socket as any).toMetaTrackEvent({ time: 1_000, track: "9", data });

    expect(event.type).toBe("event");
    expect(event.data).toBe(data);
  });

  it("renders JSON-looking subtitle text literally", () => {
    const player = new WebCodecsPlayerImpl();
    const render = vi.fn();
    (player as any).subtitleRenderer = { render, destroy: vi.fn() };
    (player as any).activeSubtitleTrackId = "7";

    const event = {
      type: "subtitle",
      timestamp: 1_000,
      trackId: "7",
      data: "true",
      durationMs: 2_000,
    };
    (player as any).renderSubtitleEvent(event);

    expect(render).toHaveBeenCalledWith(event);
    void player.destroy();
  });
});
