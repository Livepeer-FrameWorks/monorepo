import { describe, expect, it } from "vitest";
import {
  normalizeThumbnailCueTimeline,
  parseThumbnailVtt,
  type ThumbnailCue,
} from "../src/core/ThumbnailVttParser";

const cues = (ranges: Array<[number, number]>): ThumbnailCue[] =>
  ranges.map(([startTime, endTime], index) => ({
    startTime,
    endTime,
    url: "sprite.jpg",
    x: index * 160,
    y: 0,
    width: 160,
    height: 90,
  }));

describe("parseThumbnailVtt", () => {
  it("parses WebVTT timestamps with more than two hour digits", () => {
    const parsed = parseThumbnailVtt(`
WEBVTT

123:00:00.000 --> 123:00:05.000
sprite.jpg#xywh=0,0,160,90
`);

    expect(parsed).toHaveLength(1);
    expect(parsed[0].startTime).toBe(442800);
    expect(parsed[0].endTime).toBe(442805);
  });
});

describe("normalizeThumbnailCueTimeline", () => {
  it("rebases absolute Mist cue times into browser-local player time", () => {
    const normalized = normalizeThumbnailCueTimeline(
      cues([
        [940, 945],
        [995, 1000],
      ]),
      {
        isLive: true,
        seekableStartMs: 0,
        liveEdgeMs: 60_000,
        mistRangeMs: { start: 940_000, end: 1_000_000 },
      }
    );

    expect(normalized[0].startTime).toBe(0);
    expect(normalized[0].endTime).toBe(5);
    expect(normalized[1].startTime).toBe(55);
    expect(normalized[1].endTime).toBe(60);
  });

  it("rebases live-window-relative cues into the active seek range", () => {
    const normalized = normalizeThumbnailCueTimeline(
      cues([
        [0, 5],
        [55, 60],
      ]),
      {
        isLive: true,
        seekableStartMs: 940_000,
        liveEdgeMs: 1_000_000,
        mistRangeMs: { start: 940_000, end: 1_000_000 },
      }
    );

    expect(normalized[0].startTime).toBe(940);
    expect(normalized[0].endTime).toBe(945);
    expect(normalized[1].startTime).toBe(995);
    expect(normalized[1].endTime).toBe(1000);
  });

  it("extends the newest live cue to the moving live edge without shifting older cues", () => {
    const normalized = normalizeThumbnailCueTimeline(
      cues([
        [40, 45],
        [55, 60],
      ]),
      {
        isLive: true,
        seekableStartMs: 0,
        liveEdgeMs: 65_000,
        mistRangeMs: { start: 0, end: 60_000 },
      }
    );

    expect(normalized[0].startTime).toBe(40);
    expect(normalized[0].endTime).toBe(45);
    expect(normalized[1].startTime).toBe(55);
    expect(normalized[1].endTime).toBe(65);
  });

  it("keeps already-overlapping live cues in their existing player coordinates", () => {
    const normalized = normalizeThumbnailCueTimeline(
      cues([
        [540, 545],
        [595, 600],
      ]),
      {
        isLive: true,
        seekableStartMs: 550_000,
        liveEdgeMs: 610_000,
        mistRangeMs: { start: 540_000, end: 600_000 },
      }
    );

    expect(normalized[0].startTime).toBe(540);
    expect(normalized[0].endTime).toBe(545);
    expect(normalized[1].startTime).toBe(595);
    expect(normalized[1].endTime).toBe(610);
  });

  it("does not alter VOD cues", () => {
    const raw = cues([
      [0, 5],
      [5, 10],
    ]);
    expect(
      normalizeThumbnailCueTimeline(raw, {
        isLive: false,
        seekableStartMs: 0,
        liveEdgeMs: 10_000,
      })
    ).toBe(raw);
  });
});
