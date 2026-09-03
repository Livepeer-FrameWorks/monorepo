import { describe, expect, it } from "vitest";

import {
  buildAudioTrackList,
  buildSubtitleTrackList,
  buildTrackSelectionReplay,
} from "../src/core/TrackSelection";
import { PlayerController } from "../src/core/PlayerController";

describe("Mist track-list derivation", () => {
  const tracks = [
    { type: "video" as const, codec: "H264", idx: 1 },
    { type: "audio" as const, codec: "AAC", idx: 4, lang: "en" },
    { type: "audio" as const, codec: "AAC", idx: 5, lang: "es" },
    { type: "meta" as const, codec: "subtitle", idx: 8, lang: "nl" },
    { type: "meta" as const, codec: "JSON", idx: 9 },
  ];

  it("derives selectable audio ids and active state", () => {
    expect(buildAudioTrackList(tracks, "5")).toEqual([
      expect.objectContaining({ id: "4", lang: "en", active: false }),
      expect.objectContaining({ id: "5", lang: "es", active: true }),
    ]);
  });

  it("does not invent an active audio track before selection is known", () => {
    expect(buildAudioTrackList(tracks)).toEqual([
      expect.objectContaining({ id: "4", active: false }),
      expect.objectContaining({ id: "5", active: false }),
    ]);
  });

  it("includes only subtitle metadata tracks", () => {
    expect(buildSubtitleTrackList(tracks, "8")).toEqual([
      expect.objectContaining({ id: "8", lang: "nl", active: true }),
    ]);
  });

  it("ignores metadata rows whose codec is absent", () => {
    expect(buildSubtitleTrackList([{ type: "meta", idx: 9 } as any])).toEqual([]);
  });

  it("never substitutes filtered list positions for missing Mist track ids", () => {
    expect(
      buildAudioTrackList([
        { type: "video", codec: "H264", idx: 6 },
        { type: "audio", codec: "AAC", lang: "en" },
        { type: "audio", codec: "AAC", id: "audio-main", lang: "es" },
      ])
    ).toEqual([expect.objectContaining({ id: "audio-main", lang: "es" })]);
  });

  it("retains a Mist metadata record key when idx is absent", () => {
    const tracks = (PlayerController.prototype as any).parseMistTracks.call(
      {},
      {
        "11": { type: "video", codec: "H264", idx: 6 },
        "23": { type: "audio", codec: "AAC", lang: "en" },
      }
    );
    expect(buildAudioTrackList(tracks)).toEqual([
      expect.objectContaining({ id: "23", lang: "en" }),
    ]);
  });

  it("replays explicit automatic video without losing audio", () => {
    expect(
      buildTrackSelectionReplay({
        videoId: "auto",
        videoExplicit: true,
        audioId: "4",
      })
    ).toEqual({ tracks: { video: "auto", audio: "4" } });
  });
});
