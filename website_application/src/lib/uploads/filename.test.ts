import { describe, expect, it } from "vitest";
import { validUploadFilenameLength } from "./filename";

describe("upload filename length", () => {
  it("matches the API byte limit", () => {
    expect(validUploadFilenameLength("a".repeat(251) + ".mp4")).toBe(true);
    expect(validUploadFilenameLength("a".repeat(252) + ".mp4")).toBe(false);
  });
  it("counts UTF-8 bytes for Unicode filenames", () => {
    expect(validUploadFilenameLength("€".repeat(83) + ".mp4")).toBe(true);
    expect(validUploadFilenameLength("€".repeat(84) + ".mp4")).toBe(false);
  });
});
