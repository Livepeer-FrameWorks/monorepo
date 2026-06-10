import { afterEach, describe, expect, it, vi } from "vitest";

import {
  parseThumbnailVtt,
  findCueAtTime,
  normalizeThumbnailCueTimeline,
  fetchThumbnailVtt,
  type ThumbnailCue,
} from "../src/core/ThumbnailVttParser";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("parseThumbnailVtt — MM:SS timestamps + xywh", () => {
  it("parses two-part (MM:SS.mmm) timestamps and the #xywh fragment", () => {
    const vtt = `WEBVTT

00:00.000 --> 00:05.000
sprite.jpg#xywh=0,0,160,90

00:05.000 --> 00:10.000
sprite.jpg#xywh=160,0,160,90`;
    const cues = parseThumbnailVtt(vtt);
    expect(cues).toHaveLength(2);
    expect(cues[0]).toMatchObject({ startTime: 0, endTime: 5, url: "sprite.jpg", x: 0 });
    expect(cues[1]).toMatchObject({ startTime: 5, endTime: 10, x: 160, width: 160 });
  });

  it("skips blocks without a cue timing line", () => {
    expect(parseThumbnailVtt("WEBVTT\n\nNOTE just a comment")).toEqual([]);
  });
});

describe("findCueAtTime — binary search", () => {
  const cues: ThumbnailCue[] = [
    { startTime: 0, endTime: 5, url: "a" },
    { startTime: 5, endTime: 10, url: "b" },
    { startTime: 10, endTime: 15, url: "c" },
  ];

  it("returns null for an empty cue list", () => {
    expect(findCueAtTime([], 3)).toBeNull();
  });

  it("finds the cue whose [start, end) contains the time", () => {
    expect(findCueAtTime(cues, 0)?.url).toBe("a");
    expect(findCueAtTime(cues, 7)?.url).toBe("b");
    expect(findCueAtTime(cues, 14.9)?.url).toBe("c");
  });

  it("is end-exclusive at boundaries", () => {
    expect(findCueAtTime(cues, 5)?.url).toBe("b"); // 5 belongs to the second cue
  });

  it("returns null outside the covered range", () => {
    expect(findCueAtTime(cues, -1)).toBeNull();
    expect(findCueAtTime(cues, 15)).toBeNull(); // == last endTime, exclusive
  });
});

describe("normalizeThumbnailCueTimeline — guards", () => {
  const sample: ThumbnailCue[] = [{ startTime: 0, endTime: 5, url: "a" }];

  it("returns cues untouched for VOD or empty input", () => {
    expect(
      normalizeThumbnailCueTimeline(sample, { isLive: false, seekableStartMs: 0, liveEdgeMs: 0 })
    ).toBe(sample);
    expect(
      normalizeThumbnailCueTimeline([], { isLive: true, seekableStartMs: 0, liveEdgeMs: 10_000 })
    ).toEqual([]);
  });

  it("returns cues unchanged when the player window is invalid (end <= start)", () => {
    expect(
      normalizeThumbnailCueTimeline(sample, {
        isLive: true,
        seekableStartMs: 10_000,
        liveEdgeMs: 10_000,
      })
    ).toBe(sample);
  });
});

describe("fetchThumbnailVtt", () => {
  it("fetches and parses a VTT document", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        text: async () => "WEBVTT\n\n00:00.000 --> 00:05.000\nsprite.jpg#xywh=0,0,160,90",
      }))
    );
    const cues = await fetchThumbnailVtt("https://x/thumbs.vtt");
    expect(cues).toHaveLength(1);
    expect(cues[0].url).toBe("sprite.jpg");
  });

  it("throws on a non-OK response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: false, status: 404, text: async () => "" }))
    );
    await expect(fetchThumbnailVtt("https://x/missing.vtt")).rejects.toThrow("404");
  });
});
