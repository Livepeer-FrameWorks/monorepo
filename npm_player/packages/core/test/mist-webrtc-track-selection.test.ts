import { describe, expect, it, vi } from "vitest";

import { MistWebRTCPlayerImpl } from "../src/players/MistWebRTCPlayer";

describe("Mist WebRTC track selection", () => {
  function playerWithSignaling() {
    const player = new MistWebRTCPlayerImpl();
    const setTracks = vi.fn();
    (player as any).signaling = { isConnected: true, setTracks };
    (player as any).streamInfoRef = {
      meta: {
        tracks: [
          { type: "video", codec: "H264", idx: 1 },
          { type: "audio", codec: "opus", idx: 2, lang: "en" },
          { type: "audio", codec: "opus", idx: 3, lang: "es" },
          { type: "meta", codec: "subtitle", idx: 4, lang: "en" },
        ],
      },
    };
    return { player, setTracks };
  }

  it("keeps subtitle state local for controller-owned metadata rendering", () => {
    const { player, setTracks } = playerWithSignaling();
    player.selectTextTrack("4");
    player.selectTextTrack(null);
    expect(setTracks).not.toHaveBeenCalled();
  });

  it("enumerates and selects audio through the audio leg", () => {
    const { player, setTracks } = playerWithSignaling();
    expect(player.getAudioTracks()).toHaveLength(2);
    player.selectAudioTrack("3");
    expect(setTracks).toHaveBeenCalledWith({ audio: "3" });
    expect(player.getAudioTracks().find((track) => track.id === "3")?.active).toBe(true);
  });

  it("queues desired track state while signaling is down and replays it on reconnect", () => {
    const { player, setTracks } = playerWithSignaling();
    (player as any).signaling.isConnected = false;

    player.selectQuality("7");
    player.selectAudioTrack("3");
    player.selectTextTrack("4");
    player.selectTextTrack(null);
    expect(setTracks).not.toHaveBeenCalled();
    expect(player.getTextTracks().some((track) => track.active)).toBe(false);

    (player as any).signaling.isConnected = true;
    (player as any).replayDesiredTracks();
    expect(setTracks).toHaveBeenCalledTimes(1);
    expect(setTracks).toHaveBeenCalledWith({ video: "7", audio: "3" });
  });

  it("emits track-list changes from signaling time updates", () => {
    const { player } = playerWithSignaling();
    const changed = vi.fn();
    player.on("trackschange", changed);
    const video = {
      currentTime: 0,
      paused: false,
      play: vi.fn(),
      dispatchEvent: vi.fn(),
    } as any;

    (player as any).handleTimeUpdate(
      { current: 0, begin: 0, end: 0, tracks: ["1", "2"], paused: false },
      video
    );
    (player as any).handleTimeUpdate(
      { current: 0, begin: 0, end: 0, tracks: ["1", "2"], paused: false },
      video
    );

    expect(changed).toHaveBeenCalledTimes(1);
  });
});
