import { describe, expect, it } from "vitest";
import { formatDuration } from "./stream-helpers";

describe("media duration display", () => {
  it.each([
    [6.033, "00:00:06"],
    [23.978, "00:00:23"],
    [60.1, "00:01:00"],
    [3665.9, "01:01:05"],
    [0, "00:00:00"],
    [null, "N/A"],
    [undefined, "N/A"],
  ])("formats %s seconds as %s", (seconds, expected) => {
    expect(formatDuration(seconds)).toBe(expected);
  });
});
