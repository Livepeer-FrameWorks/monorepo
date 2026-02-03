import { describe, it, expect } from "vitest";
import {
  resolveTimeRange,
  timeRangeLabel,
  DEFAULT_TIME_RANGE,
  TIME_RANGE_OPTIONS,
} from "./time-range";

describe("resolveTimeRange", () => {
  const fixedNow = new Date("2024-01-15T12:00:00Z");

  it("resolves 24h time range", () => {
    const result = resolveTimeRange("24h", fixedNow);
    expect(result.value).toBe("24h");
    expect(result.label).toBe("Last 24 Hours");
    expect(result.days).toBe(1);
    expect(result.hours).toBe(24);
    expect(result.end).toBe(fixedNow.toISOString());

    const startDate = new Date(result.start);
    const expectedStart = new Date("2024-01-14T12:00:00Z");
    expect(startDate.getTime()).toBe(expectedStart.getTime());
  });

  it("resolves 7d time range", () => {
    const result = resolveTimeRange("7d", fixedNow);
    expect(result.value).toBe("7d");
    expect(result.label).toBe("Last 7 Days");
    expect(result.days).toBe(7);
    expect(result.hours).toBe(168);
  });

  it("resolves 30d time range", () => {
    const result = resolveTimeRange("30d", fixedNow);
    expect(result.value).toBe("30d");
    expect(result.label).toBe("Last 30 Days");
    expect(result.days).toBe(30);
  });

  it("resolves 90d time range", () => {
    const result = resolveTimeRange("90d", fixedNow);
    expect(result.value).toBe("90d");
    expect(result.label).toBe("Last 90 Days");
    expect(result.days).toBe(90);
  });

  it("falls back to default for unknown value", () => {
    const result = resolveTimeRange("unknown", fixedNow);
    expect(result.value).toBe(DEFAULT_TIME_RANGE);
    expect(result.days).toBe(7);
  });

  it("uses current time when no now provided", () => {
    const before = Date.now();
    const result = resolveTimeRange("7d");
    const after = Date.now();

    const endTime = new Date(result.end).getTime();
    expect(endTime).toBeGreaterThanOrEqual(before);
    expect(endTime).toBeLessThanOrEqual(after);
  });

  it("calculates start correctly for 24h range", () => {
    const result = resolveTimeRange("24h", fixedNow);
    const start = new Date(result.start);
    const end = new Date(result.end);
    const diffMs = end.getTime() - start.getTime();
    const diffHours = diffMs / (1000 * 60 * 60);
    expect(diffHours).toBe(24);
  });

  it("calculates start correctly for 7d range", () => {
    const result = resolveTimeRange("7d", fixedNow);
    const start = new Date(result.start);
    const end = new Date(result.end);
    const diffMs = end.getTime() - start.getTime();
    const diffDays = diffMs / (1000 * 60 * 60 * 24);
    expect(diffDays).toBe(7);
  });
});

describe("timeRangeLabel", () => {
  it("returns label for known values", () => {
    expect(timeRangeLabel("24h")).toBe("Last 24 Hours");
    expect(timeRangeLabel("7d")).toBe("Last 7 Days");
    expect(timeRangeLabel("30d")).toBe("Last 30 Days");
    expect(timeRangeLabel("90d")).toBe("Last 90 Days");
  });

  it("returns 'Custom Range' for unknown values", () => {
    expect(timeRangeLabel("unknown")).toBe("Custom Range");
    expect(timeRangeLabel("")).toBe("Custom Range");
  });
});

describe("TIME_RANGE_OPTIONS", () => {
  it("has expected options", () => {
    expect(TIME_RANGE_OPTIONS.length).toBe(4);
    const values = TIME_RANGE_OPTIONS.map((o) => o.value);
    expect(values).toContain("24h");
    expect(values).toContain("7d");
    expect(values).toContain("30d");
    expect(values).toContain("90d");
  });

  it("all options have required fields", () => {
    for (const option of TIME_RANGE_OPTIONS) {
      expect(option.value).toBeTruthy();
      expect(option.label).toBeTruthy();
      expect(option.days).toBeGreaterThan(0);
    }
  });
});

describe("DEFAULT_TIME_RANGE", () => {
  it("is 7d", () => {
    expect(DEFAULT_TIME_RANGE).toBe("7d");
  });

  it("exists in TIME_RANGE_OPTIONS", () => {
    const option = TIME_RANGE_OPTIONS.find((o) => o.value === DEFAULT_TIME_RANGE);
    expect(option).toBeDefined();
  });
});
