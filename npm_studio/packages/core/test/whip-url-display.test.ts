import { describe, expect, it } from "vitest";
import { formatStreamInfo, maskWhipUrl } from "../src/core/whipUrlDisplay";

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

describe("formatStreamInfo", () => {
  it("leaves the stream key out of the copied text", () => {
    const text = formatStreamInfo({
      state: "streaming",
      quality: "Broadcast (1080p @ 4.5 Mbps)",
      sourceCount: 2,
      whipUrl: "https://edge.example/webrtc/secret-key",
    });

    expect(text).not.toContain("secret-key");
    expect(text).toBe(
      [
        "Status: streaming",
        "Quality: Broadcast (1080p @ 4.5 Mbps)",
        "Sources: 2",
        "WHIP: https://edge.example/webrtc/•••",
      ].join("\n")
    );
  });

  it("has no WHIP line without an endpoint", () => {
    expect(formatStreamInfo({ state: "idle", quality: "Broadcast", sourceCount: 0 })).toBe(
      "Status: idle\nQuality: Broadcast\nSources: 0"
    );
  });
});
