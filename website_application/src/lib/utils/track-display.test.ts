import { describe, expect, it } from "vitest";

import {
  classifyTrack,
  formatTrackBitrate,
  trackDisplayName,
  trackFps,
  trackKindLabel,
  trackResolution,
} from "./track-display";

describe("track-display helpers", () => {
  it("classifies typed source video tracks", () => {
    const track = {
      trackName: "video0",
      trackType: "video",
      codec: "H264",
      width: 1920,
      height: 1080,
      fps: 24,
      bitrateKbps: 4200,
    };

    expect(classifyTrack(track)).toBe("video");
    expect(trackKindLabel(track)).toBe("Video");
    expect(trackResolution(track)).toBe("1920x1080");
    expect(formatTrackBitrate(track)).toBe("4.2 Mbps");
  });

  it("classifies raw Mist JPEG and thumbvtt tracks as generated outputs", () => {
    expect(classifyTrack({ codec: "JPEG", width: 160, height: 90 })).toBe("generated");
    expect(classifyTrack({ name: "thumbvtt", codec: "thumbvtt" })).toBe("generated");
    expect(trackDisplayName({ codec: "thumbvtt" }, 0)).toBe("Thumbnail cues");
  });

  it("parses snake_case historical track fields", () => {
    const track = {
      track_name: "audio_1",
      track_type: "audio",
      codec: "AAC",
      bitrate_kbps: 128,
      sample_rate: 48000,
    };

    expect(classifyTrack(track)).toBe("audio");
    expect(trackDisplayName(track, 0)).toBe("audio_1");
    expect(formatTrackBitrate(track)).toBe("128 kbps");
  });

  it("converts Mist fpks to fps", () => {
    expect(trackFps({ fpks: 24000 })).toBe(24);
  });

  it("hides misleading source video trickle bitrates", () => {
    expect(formatTrackBitrate({ trackType: "video", codec: "H264", bitrateKbps: 4 })).toBeNull();
  });
});
