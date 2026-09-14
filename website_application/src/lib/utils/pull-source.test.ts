import { describe, expect, it } from "vitest";
import { pullSourcePlacementClass } from "./pull-source";

describe("pullSourcePlacementClass", () => {
  it.each([
    "rtsp://192.168.1.10/live",
    "https://10.0.0.5/live.m3u8",
    "srt://172.20.1.2:9000",
    "rist://[fd00::5]:8000",
    "tsudp://239.1.2.3:9000",
  ])("requires explicit cluster pins for %s", (uri) => {
    expect(pullSourcePlacementClass(uri)).toBe("private");
  });

  it.each([
    "rtsp://camera.example.net/live",
    "https://cdn.example.net/live.m3u8",
    "tsudp://203.0.113.5:9000",
    "tsudp://video.example.net:9000",
  ])("allows automatic placement for %s", (uri) => {
    expect(pullSourcePlacementClass(uri)).toBe("public");
  });
});
