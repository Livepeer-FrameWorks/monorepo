import { describe, it, expect } from "vitest";
import { FwPlayer } from "../src/components/fw-player.js";

describe("FwPlayer", () => {
  it("is a class that extends HTMLElement", () => {
    expect(FwPlayer).toBeDefined();
    expect(FwPlayer.prototype instanceof HTMLElement).toBe(true);
  });

  it("has the expected public API methods", () => {
    const proto = FwPlayer.prototype;
    expect(typeof proto.play).toBe("function");
    expect(typeof proto.pause).toBe("function");
    expect(typeof proto.togglePlay).toBe("function");
    expect(typeof proto.seek).toBe("function");
    expect(typeof proto.seekBy).toBe("function");
    expect(typeof proto.jumpToLive).toBe("function");
    expect(typeof proto.setVolume).toBe("function");
    expect(typeof proto.toggleMute).toBe("function");
    expect(typeof proto.toggleLoop).toBe("function");
    expect(typeof proto.toggleFullscreen).toBe("function");
    expect(typeof proto.togglePiP).toBe("function");
    expect(typeof proto.toggleSubtitles).toBe("function");
    expect(typeof proto.retry).toBe("function");
    expect(typeof proto.reload).toBe("function");
    expect(typeof proto.getQualities).toBe("function");
    expect(typeof proto.selectQuality).toBe("function");
    expect(typeof proto.destroy).toBe("function");
  });
});
