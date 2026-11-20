import { describe, expect, it } from "vitest";
import { isVideoFile } from "./dnd";

describe("upload container admission", () => {
  it("accepts M2TS and MTS without a video MIME type", () => {
    for (const name of ["camera.m2ts", "camera.MTS"]) {
      expect(isVideoFile({ name, type: "" } as File)).toBe(true);
    }
  });
  it("refuses unknown containers and playlists even with a video MIME type", () => {
    for (const name of ["payload.xyz", "playlist.m3u8", "legacy.wmv", "video.mp4.exe"]) {
      expect(isVideoFile({ name, type: "video/mp4" } as File)).toBe(false);
    }
  });
});
