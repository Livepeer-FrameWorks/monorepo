import { describe, expect, it } from "vitest";
import { maskWhipUrl } from "../src/core/whipUrlDisplay";

describe("maskWhipUrl", () => {
  it("hides the stream key at the end of the path", () => {
    expect(maskWhipUrl("https://edge.example/webrtc/secret-key")).toBe(
      "https://edge.example/webrtc/•••"
    );
  });

  it("hides query and fragment credentials", () => {
    const masked = maskWhipUrl("https://edge.example/whip/key?token=secret#secret");
    expect(masked).toBe("https://edge.example/whip/•••");
  });

  it("hides embedded credentials", () => {
    expect(maskWhipUrl("https://user:secret@edge.example/webrtc/key")).toBe(
      "https://edge.example/webrtc/•••"
    );
  });

  it("hides a value it cannot parse", () => {
    expect(maskWhipUrl("not a url with a secret")).toBe("•••");
  });

  it("returns nothing for an empty value", () => {
    expect(maskWhipUrl(null)).toBe("");
    expect(maskWhipUrl(undefined)).toBe("");
  });
});
