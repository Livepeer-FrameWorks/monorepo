import { afterEach, describe, expect, it, vi } from "vitest";
import { ThumbnailSpriteManager } from "../src/core/ThumbnailSpriteManager";

const vtt = `WEBVTT

00:00:00.000 --> 00:00:05.000
sprite.jpg#xywh=0,0,160,90
`;

const nextVtt = `WEBVTT

00:00:05.000 --> 00:00:10.000
sprite.jpg#xywh=160,0,160,90
`;

function textResponse(body: string, status = 200): Response {
  return new Response(body, { status });
}

describe("ThumbnailSpriteManager", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("seeds live thumbnails from regular VTT while push mode connects", async () => {
    const fetchMock = vi.fn((url: string | URL | Request) => {
      const href = String(url);
      if (href.includes("mode=push")) {
        return Promise.resolve(textResponse("", 404));
      }
      return Promise.resolve(textResponse(vtt));
    });
    vi.stubGlobal("fetch", fetchMock);

    const events: unknown[] = [];
    const manager = new ThumbnailSpriteManager({
      vttUrl: "https://mist.example/live.thumbvtt?track=4",
      baseUrl: "https://mist.example",
      isLive: true,
      supportsPush: true,
      onCuesChange: (cues) => events.push(cues),
    });

    await vi.waitFor(() => expect(events).toHaveLength(1));

    expect(fetchMock.mock.calls[0][0]).toBe("https://mist.example/live.thumbvtt?track=4");
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("mode=push"))).toBe(true);
    expect((events[0] as any[])[0].url).toContain("_fw_thumb=");
    manager.destroy();
  });

  it("keeps polling live VTT after initial cues so stale sprites are replaced", async () => {
    const fetchMock = vi.fn((url: string | URL | Request) => {
      const href = String(url);
      if (href.includes("mode=push")) {
        return Promise.resolve(new Response(new ReadableStream(), { status: 200 }));
      }
      return Promise.resolve(textResponse(fetchMock.mock.calls.length <= 1 ? vtt : nextVtt));
    });
    vi.stubGlobal("fetch", fetchMock);

    const events: any[][] = [];
    const manager = new ThumbnailSpriteManager({
      vttUrl: "https://mist.example/live.thumbvtt?track=4",
      baseUrl: "https://mist.example",
      isLive: true,
      supportsPush: true,
      refreshInterval: 1,
      onCuesChange: (cues) => events.push(cues),
    });

    await vi.waitFor(() => expect(events.length).toBeGreaterThanOrEqual(2));

    expect(events[0][0].startTime).toBe(0);
    expect(events[1][0].startTime).toBe(5);
    expect(events[1][0].url).toContain("_fw_thumb=");
    manager.destroy();
  });

  it("retries one-shot sources until the VTT becomes available", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(textResponse("", 404))
      .mockResolvedValueOnce(textResponse(vtt));
    vi.stubGlobal("fetch", fetchMock);

    const events: unknown[] = [];
    const manager = new ThumbnailSpriteManager({
      vttUrl: "https://chandler.example/assets/key/sprite.vtt",
      baseUrl: "https://chandler.example/assets/key",
      isLive: false,
      refreshInterval: 1,
      onCuesChange: (cues) => events.push(cues),
    });

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(events).toHaveLength(0);

    await vi.waitFor(() => expect(events).toHaveLength(1));
    expect(fetchMock).toHaveBeenCalledTimes(2);
    manager.destroy();
  });

  it("does not request push mode for live Chandler sprite VTT URLs", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(textResponse(vtt)));
    vi.stubGlobal("fetch", fetchMock);

    const events: unknown[] = [];
    const manager = new ThumbnailSpriteManager({
      vttUrl: "https://chandler.example/assets/key/sprite.vtt",
      baseUrl: "https://chandler.example/assets/key",
      isLive: true,
      refreshInterval: 1000,
      onCuesChange: (cues) => events.push(cues),
    });

    await vi.waitFor(() => expect(events).toHaveLength(1));

    expect(fetchMock.mock.calls.every(([url]) => !String(url).includes("mode=push"))).toBe(true);
    manager.destroy();
  });
});
