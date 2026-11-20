import { describe, expect, it } from "vitest";
import { protocolLabel } from "./protocols";

describe("analytics protocol labels", () => {
  it("names subtitle viewing WebVTT", () => {
    expect(protocolLabel("webvtt")).toBe("WebVTT");
    expect(protocolLabel("WEBVTT")).toBe("WebVTT");
    expect(protocolLabel("hls")).toBe("HLS");
  });
});
