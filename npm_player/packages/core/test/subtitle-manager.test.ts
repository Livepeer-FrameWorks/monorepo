import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { SubtitleManager, type SubtitleTrackInfo } from "../src/core/SubtitleManager";

// ---------------------------------------------------------------------------
// Mock DOM helpers
// ---------------------------------------------------------------------------

function createMockVideo() {
  const tracks: any[] = [];
  const trackElements: any[] = [];
  const listeners = new Map<string, Function[]>();

  return {
    textTracks: tracks as unknown as TextTrackList,
    addEventListener: vi.fn((event: string, handler: Function) => {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event)!.push(handler);
    }),
    removeEventListener: vi.fn((event: string, _handler: Function) => {
      listeners.delete(event);
    }),
    querySelectorAll: vi.fn((_selector: string) => trackElements),
    appendChild: vi.fn((el: any) => {
      trackElements.push(el);
      tracks.push({ mode: "disabled", cues: null });
    }),
    parentElement: null,
    _trackElements: trackElements,
    _listeners: listeners,
  } as unknown as HTMLVideoElement;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("SubtitleManager", () => {
  let origDocument: PropertyDescriptor | undefined;

  beforeEach(() => {
    vi.spyOn(console, "debug").mockImplementation(() => {});

    origDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
    Object.defineProperty(globalThis, "document", {
      value: {
        createElement: vi.fn((tag: string) => ({
          tagName: tag.toUpperCase(),
          kind: "",
          label: "",
          srclang: "",
          src: "",
          default: false,
          addEventListener: vi.fn(),
          remove: vi.fn(),
        })),
      },
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    if (origDocument) {
      Object.defineProperty(globalThis, "document", origDocument);
    }
  });

  // ===========================================================================
  // Constructor
  // ===========================================================================
  describe("constructor", () => {
    it("creates with default config", () => {
      const mgr = new SubtitleManager();
      expect(mgr.getCurrentTrackId()).toBeNull();
    });

    it("creates with custom config", () => {
      const mgr = new SubtitleManager({
        mistBaseUrl: "https://mist.example.com",
        streamName: "live",
        urlAppend: "token=abc",
        debug: true,
      });
      expect(mgr.getCurrentTrackId()).toBeNull();
    });
  });

  // ===========================================================================
  // Static: createTrackInfo
  // ===========================================================================
  describe("createTrackInfo", () => {
    it("builds WebVTT URL from parts", () => {
      const info = SubtitleManager.createTrackInfo(
        "track1",
        "English",
        "en",
        "https://mist.example.com",
        "live"
      );

      expect(info.id).toBe("track1");
      expect(info.label).toBe("English");
      expect(info.lang).toBe("en");
      expect(info.src).toBe("https://mist.example.com/live.vtt?track=track1");
    });
  });

  // ===========================================================================
  // Static: parseTracksFromStreamInfo
  // ===========================================================================
  describe("parseTracksFromStreamInfo", () => {
    it("returns empty for no tracks", () => {
      const result = SubtitleManager.parseTracksFromStreamInfo(
        {},
        "https://mist.example.com",
        "live"
      );
      expect(result).toEqual([]);
    });

    it("returns empty for no meta", () => {
      const result = SubtitleManager.parseTracksFromStreamInfo(
        { meta: {} },
        "https://mist.example.com",
        "live"
      );
      expect(result).toEqual([]);
    });

    it("filters subtitle tracks only", () => {
      const result = SubtitleManager.parseTracksFromStreamInfo(
        {
          meta: {
            tracks: {
              video1: { type: "video", codec: "H264" },
              audio1: { type: "audio", codec: "AAC" },
              sub1: { type: "meta", codec: "subtitle", lang: "en" },
              sub2: { type: "meta", codec: "subtitle", lang: "es" },
            },
          },
        },
        "https://mist.example.com",
        "live"
      );

      expect(result).toHaveLength(2);
      expect(result[0].id).toBe("sub1");
      expect(result[0].lang).toBe("en");
      expect(result[0].label).toBe("EN");
      expect(result[1].id).toBe("sub2");
      expect(result[1].lang).toBe("es");
    });

    it("uses 'und' fallback for missing lang", () => {
      const result = SubtitleManager.parseTracksFromStreamInfo(
        {
          meta: {
            tracks: {
              sub1: { type: "meta", codec: "subtitle" },
            },
          },
        },
        "https://mist.example.com",
        "live"
      );

      expect(result[0].lang).toBe("und");
      expect(result[0].label).toBe("Subtitles sub1");
    });
  });

  // ===========================================================================
  // attach / detach
  // ===========================================================================
  describe("attach / detach", () => {
    it("attach registers event listeners", () => {
      const mgr = new SubtitleManager();
      const video = createMockVideo();

      mgr.attach(video);
      expect(video.addEventListener).toHaveBeenCalledWith("loadeddata", expect.any(Function));
      expect(video.addEventListener).toHaveBeenCalledWith("seeked", expect.any(Function));
    });

    it("detach removes event listeners", () => {
      const mgr = new SubtitleManager();
      const video = createMockVideo();

      mgr.attach(video);
      mgr.detach();

      expect(video.removeEventListener).toHaveBeenCalledWith("loadeddata", expect.any(Function));
      expect(video.removeEventListener).toHaveBeenCalledWith("seeked", expect.any(Function));
    });

    it("detach clears currentTrackId", () => {
      const mgr = new SubtitleManager();
      const video = createMockVideo();

      mgr.attach(video);
      mgr.detach();
      expect(mgr.getCurrentTrackId()).toBeNull();
    });

    it("re-attach detaches old video first", () => {
      const mgr = new SubtitleManager();
      const video1 = createMockVideo();
      const video2 = createMockVideo();

      mgr.attach(video1);
      mgr.attach(video2);

      expect(video1.removeEventListener).toHaveBeenCalled();
      expect(video2.addEventListener).toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // getTextTracks / getTrackElements
  // ===========================================================================
  describe("track accessors", () => {
    it("getTextTracks returns empty when no video attached", () => {
      const mgr = new SubtitleManager();
      expect(mgr.getTextTracks()).toEqual([]);
    });

    it("getTrackElements returns empty when no video attached", () => {
      const mgr = new SubtitleManager();
      expect(mgr.getTrackElements()).toEqual([]);
    });
  });

  // ===========================================================================
  // setSubtitle
  // ===========================================================================
  describe("setSubtitle", () => {
    it("no-op when no video attached", () => {
      const mgr = new SubtitleManager({ debug: true });
      mgr.setSubtitle({ id: "1", label: "EN", lang: "en", src: "/test.vtt" });
      expect(mgr.getCurrentTrackId()).toBeNull();
    });

    it("sets currentTrackId on valid track", () => {
      const mgr = new SubtitleManager();
      const video = createMockVideo();
      mgr.attach(video);

      mgr.setSubtitle({ id: "sub1", label: "EN", lang: "en", src: "/test.vtt" });
      expect(mgr.getCurrentTrackId()).toBe("sub1");
    });

    it("null disables subtitles", () => {
      const mgr = new SubtitleManager();
      const video = createMockVideo();
      mgr.attach(video);

      mgr.setSubtitle({ id: "sub1", label: "EN", lang: "en", src: "/test.vtt" });
      mgr.setSubtitle(null);
      expect(mgr.getCurrentTrackId()).toBeNull();
    });

    it("creates track element via document.createElement", () => {
      const mgr = new SubtitleManager();
      const video = createMockVideo();
      mgr.attach(video);

      mgr.setSubtitle({ id: "sub1", label: "EN", lang: "en", src: "/test.vtt" });
      expect(document.createElement).toHaveBeenCalledWith("track");
      expect(video.appendChild).toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // buildTrackUrl (via setSubtitle)
  // ===========================================================================
  describe("URL building", () => {
    it("absolute URL used as-is", () => {
      const mgr = new SubtitleManager({ mistBaseUrl: "https://mist.example.com" });
      const video = createMockVideo();
      mgr.attach(video);

      mgr.setSubtitle({
        id: "1",
        label: "EN",
        lang: "en",
        src: "https://cdn.example.com/sub.vtt",
      });

      const trackEl = (video.appendChild as any).mock.calls[0][0];
      expect(trackEl.src).toBe("https://cdn.example.com/sub.vtt");
    });

    it("relative URL prepended with base", () => {
      const mgr = new SubtitleManager({ mistBaseUrl: "https://mist.example.com" });
      const video = createMockVideo();
      mgr.attach(video);

      mgr.setSubtitle({ id: "1", label: "EN", lang: "en", src: "/live.vtt?track=1" });

      const trackEl = (video.appendChild as any).mock.calls[0][0];
      expect(trackEl.src).toBe("https://mist.example.com/live.vtt?track=1");
    });

    it("relative URL without slash prepended with base/", () => {
      const mgr = new SubtitleManager({ mistBaseUrl: "https://mist.example.com/" });
      const video = createMockVideo();
      mgr.attach(video);

      mgr.setSubtitle({ id: "1", label: "EN", lang: "en", src: "live.vtt" });

      const trackEl = (video.appendChild as any).mock.calls[0][0];
      expect(trackEl.src).toBe("https://mist.example.com/live.vtt");
    });

    it("appends urlAppend with ? separator", () => {
      const mgr = new SubtitleManager({
        mistBaseUrl: "https://mist.example.com",
        urlAppend: "token=abc",
      });
      const video = createMockVideo();
      mgr.attach(video);

      mgr.setSubtitle({ id: "1", label: "EN", lang: "en", src: "/live.vtt" });

      const trackEl = (video.appendChild as any).mock.calls[0][0];
      expect(trackEl.src).toBe("https://mist.example.com/live.vtt?token=abc");
    });

    it("appends urlAppend with & when URL has query string", () => {
      const mgr = new SubtitleManager({
        mistBaseUrl: "https://mist.example.com",
        urlAppend: "token=abc",
      });
      const video = createMockVideo();
      mgr.attach(video);

      mgr.setSubtitle({ id: "1", label: "EN", lang: "en", src: "/live.vtt?track=1" });

      const trackEl = (video.appendChild as any).mock.calls[0][0];
      expect(trackEl.src).toBe("https://mist.example.com/live.vtt?track=1&token=abc");
    });
  });

  // ===========================================================================
  // setSeekOffset
  // ===========================================================================
  describe("setSeekOffset", () => {
    it("stores offset value", () => {
      const mgr = new SubtitleManager();
      mgr.setSeekOffset(5);
      // No public getter for seekOffset, but it shouldn't throw
    });

    it("no error when no video attached", () => {
      const mgr = new SubtitleManager();
      expect(() => mgr.setSeekOffset(10)).not.toThrow();
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("calls detach", () => {
      const mgr = new SubtitleManager();
      const video = createMockVideo();
      mgr.attach(video);
      mgr.destroy();

      expect(video.removeEventListener).toHaveBeenCalled();
      expect(mgr.getCurrentTrackId()).toBeNull();
    });

    it("safe to call on fresh instance", () => {
      const mgr = new SubtitleManager();
      expect(() => mgr.destroy()).not.toThrow();
    });
  });
});
