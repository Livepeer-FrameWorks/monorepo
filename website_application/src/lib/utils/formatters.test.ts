import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  formatBytes,
  formatDuration,
  formatDate,
  formatExpiry,
  isExpired,
  formatNumber,
  formatPercentage,
  formatBitrate,
  formatResolution,
  formatCurrency,
  formatUptime,
  decodeRelayId,
} from "./formatters";

describe("formatBytes", () => {
  it("returns '0 Bytes' for 0", () => {
    expect(formatBytes(0)).toBe("0 Bytes");
  });

  it("returns '0 Bytes' for falsy values", () => {
    expect(formatBytes(undefined as unknown as number)).toBe("0 Bytes");
  });

  it("formats bytes correctly", () => {
    expect(formatBytes(500)).toBe("500 Bytes");
  });

  it("formats kilobytes correctly", () => {
    expect(formatBytes(1024)).toBe("1 KB");
    expect(formatBytes(1536)).toBe("1.5 KB");
  });

  it("formats megabytes correctly", () => {
    expect(formatBytes(1048576)).toBe("1 MB");
  });

  it("formats gigabytes correctly", () => {
    expect(formatBytes(1073741824)).toBe("1 GB");
  });

  it("respects decimal places", () => {
    expect(formatBytes(1536, 0)).toBe("2 KB");
    expect(formatBytes(1536, 3)).toBe("1.5 KB");
  });
});

describe("formatDuration", () => {
  it("returns '0s' for 0", () => {
    expect(formatDuration(0)).toBe("0s");
  });

  it("formats seconds only", () => {
    expect(formatDuration(45)).toBe("45s");
  });

  it("formats minutes and seconds", () => {
    expect(formatDuration(125)).toBe("2m 5s");
  });

  it("formats hours, minutes, and seconds", () => {
    expect(formatDuration(3665)).toBe("1h 1m 5s");
  });
});

describe("formatDate", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2024-01-15T12:00:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns 'N/A' for falsy input", () => {
    expect(formatDate("")).toBe("N/A");
  });

  it("returns 'Invalid Date' for invalid date", () => {
    expect(formatDate("not-a-date")).toBe("Invalid Date");
  });

  it("returns 'Just now' for less than a minute ago", () => {
    const date = new Date("2024-01-15T11:59:30Z");
    expect(formatDate(date)).toBe("Just now");
  });

  it("formats minutes ago", () => {
    const date = new Date("2024-01-15T11:30:00Z");
    expect(formatDate(date)).toBe("30m ago");
  });

  it("formats hours ago", () => {
    const date = new Date("2024-01-15T10:00:00Z");
    expect(formatDate(date)).toBe("2h ago");
  });

  it("formats days ago", () => {
    const date = new Date("2024-01-13T12:00:00Z");
    expect(formatDate(date)).toBe("2d ago");
  });
});

describe("formatExpiry", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2024-01-15T12:00:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns 'Never' for null/undefined", () => {
    expect(formatExpiry(null)).toBe("Never");
    expect(formatExpiry(undefined)).toBe("Never");
  });

  it("returns 'Expired' for past dates", () => {
    const past = new Date("2024-01-14T12:00:00Z");
    expect(formatExpiry(past)).toBe("Expired");
  });

  it("formats future minutes", () => {
    const future = new Date("2024-01-15T12:30:00Z");
    expect(formatExpiry(future)).toBe("in 30m");
  });

  it("formats future hours", () => {
    const future = new Date("2024-01-15T15:00:00Z");
    expect(formatExpiry(future)).toBe("in 3h");
  });

  it("formats future days", () => {
    const future = new Date("2024-01-18T12:00:00Z");
    expect(formatExpiry(future)).toBe("in 3d");
  });
});

describe("isExpired", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2024-01-15T12:00:00Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns false for null/undefined", () => {
    expect(isExpired(null)).toBe(false);
    expect(isExpired(undefined)).toBe(false);
  });

  it("returns true for past dates", () => {
    expect(isExpired(new Date("2024-01-14T12:00:00Z"))).toBe(true);
  });

  it("returns false for future dates", () => {
    expect(isExpired(new Date("2024-01-16T12:00:00Z"))).toBe(false);
  });

  it("returns false for invalid dates", () => {
    expect(isExpired("not-a-date")).toBe(false);
  });
});

describe("formatNumber", () => {
  it("returns 'N/A' for null/undefined/NaN", () => {
    expect(formatNumber(null as unknown as number)).toBe("N/A");
    expect(formatNumber(undefined as unknown as number)).toBe("N/A");
    expect(formatNumber(NaN)).toBe("N/A");
  });

  it("formats numbers with commas", () => {
    expect(formatNumber(1000)).toBe("1,000");
    expect(formatNumber(1000000)).toBe("1,000,000");
  });

  it("formats small numbers", () => {
    expect(formatNumber(42)).toBe("42");
  });
});

describe("formatPercentage", () => {
  it("returns '0%' for zero values", () => {
    expect(formatPercentage(0, 100)).toBe("0%");
    expect(formatPercentage(50, 0)).toBe("0%");
  });

  it("calculates percentage correctly", () => {
    expect(formatPercentage(50, 100)).toBe("50.0%");
    expect(formatPercentage(1, 3, 2)).toBe("33.33%");
  });
});

describe("formatBitrate", () => {
  it("returns '0 kbps' for zero", () => {
    expect(formatBitrate(0)).toBe("0 kbps");
  });

  it("formats kbps", () => {
    expect(formatBitrate(500)).toBe("500 kbps");
  });

  it("formats Mbps", () => {
    expect(formatBitrate(2500)).toBe("2.5 Mbps");
    expect(formatBitrate(1000)).toBe("1.0 Mbps");
  });
});

describe("formatResolution", () => {
  it("returns 'N/A' for empty input", () => {
    expect(formatResolution("")).toBe("N/A");
  });

  it("maps known resolutions", () => {
    expect(formatResolution("1920x1080")).toBe("1080p");
    expect(formatResolution("1280x720")).toBe("720p");
    expect(formatResolution("3840x2160")).toBe("4K");
  });

  it("returns unknown resolutions as-is", () => {
    expect(formatResolution("1600x900")).toBe("1600x900");
  });
});

describe("formatCurrency", () => {
  it("returns 'N/A' for invalid values", () => {
    expect(formatCurrency(null as unknown as number)).toBe("N/A");
    expect(formatCurrency(NaN)).toBe("N/A");
  });

  it("formats USD by default", () => {
    const result = formatCurrency(99.99);
    expect(result).toContain("99.99");
  });

  it("formats other currencies", () => {
    const result = formatCurrency(100, "EUR");
    expect(result).toContain("100");
  });
});

describe("formatUptime", () => {
  it("returns '0s' for zero", () => {
    expect(formatUptime(0)).toBe("0s");
  });

  it("formats seconds", () => {
    expect(formatUptime(5000)).toBe("5s");
  });

  it("formats minutes and seconds", () => {
    expect(formatUptime(125000)).toBe("2m 5s");
  });

  it("formats hours, minutes, seconds", () => {
    expect(formatUptime(3665000)).toBe("1h 1m 5s");
  });

  it("formats days", () => {
    expect(formatUptime(90000000)).toBe("1d 1h");
  });
});

describe("decodeRelayId", () => {
  it("returns null for null/undefined", () => {
    expect(decodeRelayId(null)).toBe(null);
    expect(decodeRelayId(undefined)).toBe(null);
  });

  it("decodes valid base64 relay IDs", () => {
    const encoded = btoa("Stream:abc123");
    expect(decodeRelayId(encoded)).toBe("abc123");
  });

  it("validates expected type", () => {
    const encoded = btoa("Stream:abc123");
    expect(decodeRelayId(encoded, "Stream")).toBe("abc123");
    expect(decodeRelayId(encoded, "Clip")).toBe(encoded);
  });

  it("returns value as-is for invalid base64", () => {
    expect(decodeRelayId("not-base64!!!")).toBe("not-base64!!!");
  });

  it("returns value as-is for missing colon", () => {
    const encoded = btoa("StreamABC");
    expect(decodeRelayId(encoded)).toBe(encoded);
  });
});
