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

  it("routes dash.js live DVR null-range rejections through onError", () => {
    const player = new DashJsPlayerImpl();
    const errors: string[] = [];
    let prevented = false;
    let stopped = false;

    const handler = (player as any).createInternalRejectionHandler({
      onError: (error: string | Error) => errors.push(String(error)),
    });

    handler({
      reason: new TypeError(`can't access property "range", v.getCurrentDVRInfo() is null`),
      preventDefault: () => {
        prevented = true;
      },
      stopImmediatePropagation: () => {
        stopped = true;
      },
    } as PromiseRejectionEvent);

    expect(prevented).toBe(true);
    expect(stopped).toBe(true);
    expect(errors).toEqual([
      `DASH fatal internal error: can't access property "range", v.getCurrentDVRInfo() is null`,
    ]);
  });
});
