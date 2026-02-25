import { describe, it, expect } from "vitest";
import {
  formatTime,
  formatClockTime,
  formatTimeDisplay,
  formatTooltipTime,
  formatDuration,
  parseTime,
} from "../src/core/TimeFormat";

describe("formatTime", () => {
  it("returns 'LIVE' for negative values", () => {
    expect(formatTime(-1)).toBe("LIVE");
    expect(formatTime(-100)).toBe("LIVE");
  });

  it("returns 'LIVE' for NaN", () => {
    expect(formatTime(NaN)).toBe("LIVE");
  });

  it("returns 'LIVE' for Infinity", () => {
    expect(formatTime(Infinity)).toBe("LIVE");
  });

  it("formats seconds only", () => {
    expect(formatTime(0)).toBe("00:00");
    expect(formatTime(5000)).toBe("00:05");
    expect(formatTime(59000)).toBe("00:59");
  });

  it("formats minutes and seconds", () => {
    expect(formatTime(60000)).toBe("01:00");
    expect(formatTime(65000)).toBe("01:05");
    expect(formatTime(125000)).toBe("02:05");
  });

  it("formats hours, minutes, seconds", () => {
    expect(formatTime(3600000)).toBe("1:00:00");
    expect(formatTime(3665000)).toBe("1:01:05");
    expect(formatTime(36000000)).toBe("10:00:00");
  });

  it("floors fractional milliseconds", () => {
    expect(formatTime(65900)).toBe("01:05");
  });
});

describe("formatClockTime", () => {
  it("formats date as HH:MM:SS", () => {
    const date = new Date("2024-01-15T14:30:45");
    expect(formatClockTime(date)).toBe("14:30:45");
  });

  it("pads single digits", () => {
    const date = new Date("2024-01-15T01:02:03");
    expect(formatClockTime(date)).toBe("01:02:03");
  });

  it("handles midnight", () => {
    const date = new Date("2024-01-15T00:00:00");
    expect(formatClockTime(date)).toBe("00:00:00");
  });
});

describe("formatTimeDisplay", () => {
  it("shows wall-clock time for live with unixoffset", () => {
    const result = formatTimeDisplay({
      isLive: true,
      currentTime: 60000,
      duration: Infinity,
      liveEdge: 60000,
      seekableStart: 0,
      unixoffset: new Date("2024-01-15T14:29:00Z").getTime(),
    });
    expect(result).toMatch(/^\d{2}:\d{2}:\d{2}$/);
  });

  it("shows 'LIVE' when at live edge", () => {
    const result = formatTimeDisplay({
      isLive: true,
      currentTime: 60000,
      duration: Infinity,
      liveEdge: 60000,
      seekableStart: 0,
    });
    expect(result).toBe("LIVE");
  });

  it("shows negative time when behind live", () => {
    const result = formatTimeDisplay({
      isLive: true,
      currentTime: 50000,
      duration: Infinity,
      liveEdge: 60000,
      seekableStart: 0,
    });
    expect(result).toBe("-00:10");
  });

  it("shows current / duration for VOD", () => {
    const result = formatTimeDisplay({
      isLive: false,
      currentTime: 65000,
      duration: 300000,
      liveEdge: 300000,
      seekableStart: 0,
    });
    expect(result).toBe("01:05 / 05:00");
  });

  it("shows only current time for VOD without valid duration", () => {
    const result = formatTimeDisplay({
      isLive: false,
      currentTime: 65000,
      duration: 0,
      liveEdge: 0,
      seekableStart: 0,
    });
    expect(result).toBe("01:05");
  });
});

describe("formatTooltipTime", () => {
  it("formats VOD time normally", () => {
    expect(formatTooltipTime(65000, false)).toBe("01:05");
  });

  it("shows 'LIVE' when at live edge", () => {
    expect(formatTooltipTime(60000, true, 60000)).toBe("LIVE");
  });

  it("shows negative time when behind live", () => {
    expect(formatTooltipTime(50000, true, 60000)).toBe("-00:10");
  });

  it("formats normally when no live edge provided", () => {
    expect(formatTooltipTime(65000, true)).toBe("01:05");
  });
});

describe("formatDuration", () => {
  it("returns 'LIVE' for live content", () => {
    expect(formatDuration(300, true)).toBe("LIVE");
  });

  it("returns 'LIVE' for infinite duration", () => {
    expect(formatDuration(Infinity)).toBe("LIVE");
  });

  it("formats finite duration", () => {
    expect(formatDuration(300000, false)).toBe("05:00");
    expect(formatDuration(3665000)).toBe("1:01:05");
  });
});

describe("parseTime", () => {
  it("parses MM:SS format", () => {
    expect(parseTime("01:30")).toBe(90);
    expect(parseTime("05:00")).toBe(300);
  });

  it("parses HH:MM:SS format", () => {
    expect(parseTime("1:30:45")).toBe(5445);
    expect(parseTime("01:00:00")).toBe(3600);
  });

  it("returns NaN for invalid input", () => {
    expect(parseTime("invalid")).toBe(NaN);
    expect(parseTime("")).toBe(NaN);
    expect(parseTime("1:2:3:4")).toBe(NaN);
  });

  it("returns NaN for non-numeric parts", () => {
    expect(parseTime("ab:cd")).toBe(NaN);
  });
});
