import { describe, expect, it } from "vitest";

import { MistPlayerImpl } from "../src/players/MistPlayer";
import type { StreamInfo, StreamSource } from "../src/core/PlayerInterface";

const stream: StreamInfo = { source: [], meta: { tracks: [] }, type: "live" };
const callPrivate = (p: MistPlayerImpl, name: string, ...args: unknown[]) =>
  (p as any)[name](...args);

describe("MistPlayerImpl — capability gate", () => {
  it("only matches the synthetic mist/legacy type", () => {
    const p = new MistPlayerImpl();
    expect(p.isMimeSupported("mist/legacy")).toBe(true);
    expect(p.isMimeSupported("html5/video/mp4")).toBe(false);
  });

  it("isBrowserSupported claims video+audio for mist/legacy, false otherwise", () => {
    const p = new MistPlayerImpl();
    const source = { type: "mist/legacy", url: "https://x/stream" } as StreamSource;
    expect(p.isBrowserSupported("mist/legacy", source, stream)).toEqual(["video", "audio"]);
    expect(p.isBrowserSupported("html5/video/mp4", source, stream)).toBe(false);
  });
});

describe("MistPlayerImpl — getPlayerJsUrl", () => {
  it("prefers an explicit mistPlayerUrl override", () => {
    const p = new MistPlayerImpl();
    const source = {
      type: "mist/legacy",
      url: "https://edge.example/hls/x.m3u8",
      mistPlayerUrl: "https://cdn.example/custom-player.js",
    } as StreamSource;
    expect(callPrivate(p, "getPlayerJsUrl", source)).toBe("https://cdn.example/custom-player.js");
  });

  it("derives player.js from the source origin (protocol + host)", () => {
    const p = new MistPlayerImpl();
    const source = {
      type: "mist/legacy",
      url: "https://edge.example:8080/view/abc.html",
    } as StreamSource;
    expect(callPrivate(p, "getPlayerJsUrl", source)).toBe("https://edge.example:8080/player.js");
  });

  it("falls back to a relative path for an unparseable URL", () => {
    const p = new MistPlayerImpl();
    const source = { type: "mist/legacy", url: "not a url" } as StreamSource;
    expect(callPrivate(p, "getPlayerJsUrl", source)).toBe("/player.js");
  });
});
