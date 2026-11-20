import { describe, expect, it } from "vitest";
import { ladderSummary } from "./ladders";

describe("ladderSummary", () => {
  it("lists sizes and names the high-frame-rate renditions once", () => {
    expect(ladderSummary(["360p", "720p", "720p60", "1080p", "1080p60"])).toBe(
      "360p, 720p, 1080p, with higher-bitrate renditions for 50 fps and faster sources"
    );
  });
  it("lists a ladder without high-frame-rate renditions as is", () => {
    expect(ladderSummary(["360p", "720p"])).toBe("360p, 720p");
  });
});
