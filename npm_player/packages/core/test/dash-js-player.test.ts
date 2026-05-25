// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { DashJsPlayerImpl } from "../src/players/DashJsPlayer";

describe("DashJsPlayerImpl", () => {
  it("reads proxied media properties with the native video element receiver", () => {
    const player = new DashJsPlayerImpl();
    const video = document.createElement("video");

    Object.defineProperty(video, "duration", {
      configurable: true,
      get() {
        if (this !== video) {
          throw new TypeError("duration getter called with wrong receiver");
        }
        return 42;
      },
    });

    const proxy = (player as any).createVideoProxy(video) as HTMLVideoElement;

    expect(proxy.duration).toBe(42);
  });
});
