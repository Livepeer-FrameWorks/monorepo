import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { WhipClient } from "../src/core/WhipClient";

function makeClient(overrides: Record<string, unknown> = {}) {
  return new WhipClient({
    whipUrl: "https://whip.example.com/ingest",
    debug: false,
    ...overrides,
  } as any);
}

describe("WhipClient", () => {
  let origRTCRtpScriptTransform: unknown;

  beforeEach(() => {
    origRTCRtpScriptTransform = (globalThis as any).RTCRtpScriptTransform;
  });

  afterEach(() => {
    if (origRTCRtpScriptTransform !== undefined) {
      (globalThis as any).RTCRtpScriptTransform = origRTCRtpScriptTransform;
    } else {
      delete (globalThis as any).RTCRtpScriptTransform;
    }
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Initial state
  // ===========================================================================
  describe("initial state", () => {
    it("starts disconnected", () => {
      const client = makeClient();
      expect(client.getState()).toBe("disconnected");
      expect(client.isConnected).toBe(false);
    });

    it("has no peer connection", () => {
      const client = makeClient();
      expect(client.getPeerConnection()).toBeNull();
    });

    it("has no negotiated codecs", () => {
      const client = makeClient();
      expect(client.getNegotiatedVideoCodec()).toBeNull();
      expect(client.getNegotiatedAudioCodec()).toBeNull();
    });

    it("has no encoder transform", () => {
      const client = makeClient();
      expect(client.hasEncoderTransform()).toBe(false);
    });
  });

  // ===========================================================================
  // canUseEncodedInsertion
  // ===========================================================================
  describe("canUseEncodedInsertion", () => {
    it("returns false when not connected", () => {
      const client = makeClient();
      expect(client.canUseEncodedInsertion()).toBe(false);
    });

    it("returns false when no RTCRtpScriptTransform", () => {
      delete (globalThis as any).RTCRtpScriptTransform;
      const client = makeClient();
      (client as any).state = "connected";
      (client as any).peerConnection = { getSenders: () => [{ transform: {} }] };
      expect(client.canUseEncodedInsertion()).toBe(false);
    });

    it("returns true when connected + transform supported", () => {
      (globalThis as any).RTCRtpScriptTransform = function () {};
      const client = makeClient();
      (client as any).state = "connected";
      (client as any).peerConnection = { getSenders: () => [{ transform: {} }] };
      expect(client.canUseEncodedInsertion()).toBe(true);
    });

    it("returns false when senders lack transform support", () => {
      (globalThis as any).RTCRtpScriptTransform = function () {};
      const client = makeClient();
      (client as any).state = "connected";
      (client as any).peerConnection = { getSenders: () => [{}] };
      expect(client.canUseEncodedInsertion()).toBe(false);
    });

    it("returns false when video codec is incompatible", () => {
      (globalThis as any).RTCRtpScriptTransform = function () {};
      const client = makeClient();
      (client as any).state = "connected";
      (client as any).peerConnection = { getSenders: () => [{ transform: {} }] };
      (client as any).negotiatedVideoCodec = "video/AV1";
      expect(client.canUseEncodedInsertion()).toBe(false);
    });

    it("returns true when video codec is VP9", () => {
      (globalThis as any).RTCRtpScriptTransform = function () {};
      const client = makeClient();
      (client as any).state = "connected";
      (client as any).peerConnection = { getSenders: () => [{ transform: {} }] };
      (client as any).negotiatedVideoCodec = "video/VP9";
      expect(client.canUseEncodedInsertion()).toBe(true);
    });

    it("returns true when video codec is H264", () => {
      (globalThis as any).RTCRtpScriptTransform = function () {};
      const client = makeClient();
      (client as any).state = "connected";
      (client as any).peerConnection = { getSenders: () => [{ transform: {} }] };
      (client as any).negotiatedVideoCodec = "video/H264";
      expect(client.canUseEncodedInsertion()).toBe(true);
    });

    it("returns false when audio codec is not opus", () => {
      (globalThis as any).RTCRtpScriptTransform = function () {};
      const client = makeClient();
      (client as any).state = "connected";
      (client as any).peerConnection = { getSenders: () => [{ transform: {} }] };
      (client as any).negotiatedAudioCodec = "audio/G722";
      expect(client.canUseEncodedInsertion()).toBe(false);
    });

    it("returns true when audio codec is opus", () => {
      (globalThis as any).RTCRtpScriptTransform = function () {};
      const client = makeClient();
      (client as any).state = "connected";
      (client as any).peerConnection = { getSenders: () => [{ transform: {} }] };
      (client as any).negotiatedAudioCodec = "audio/opus";
      expect(client.canUseEncodedInsertion()).toBe(true);
    });
  });

  // ===========================================================================
  // replaceTrack / addTrack
  // ===========================================================================
  describe("track management", () => {
    it("replaceTrack throws when no peer connection", async () => {
      const client = makeClient();
      const track = { kind: "video" } as MediaStreamTrack;
      await expect(client.replaceTrack(track, track)).rejects.toThrow("No peer connection");
    });

    it("addTrack throws when no peer connection", async () => {
      const client = makeClient();
      const track = { kind: "video" } as MediaStreamTrack;
      await expect(client.addTrack(track)).rejects.toThrow("No peer connection");
    });

    it("replaceTrack calls sender.replaceTrack", async () => {
      const replaceTrackFn = vi.fn();
      const client = makeClient();
      (client as any).peerConnection = {
        getSenders: () => [{ track: { kind: "video" }, replaceTrack: replaceTrackFn }],
      };

      const oldTrack = { kind: "video" } as MediaStreamTrack;
      const newTrack = { kind: "video" } as MediaStreamTrack;
      await client.replaceTrack(oldTrack, newTrack);
      expect(replaceTrackFn).toHaveBeenCalledWith(newTrack);
    });

    it("replaceTrack throws when no sender for track kind", async () => {
      const client = makeClient();
      (client as any).peerConnection = {
        getSenders: () => [{ track: { kind: "audio" } }],
      };

      const track = { kind: "video" } as MediaStreamTrack;
      await expect(client.replaceTrack(track, track)).rejects.toThrow(
        "No sender found for video track"
      );
    });
  });

  // ===========================================================================
  // getStats
  // ===========================================================================
  describe("getStats", () => {
    it("returns null when no peer connection", async () => {
      const client = makeClient();
      expect(await client.getStats()).toBeNull();
    });

    it("returns stats from peer connection", async () => {
      const mockStats = new Map();
      const client = makeClient();
      (client as any).peerConnection = {
        getStats: vi.fn(async () => mockStats),
      };
      expect(await client.getStats()).toBe(mockStats);
    });

    it("returns null on stats error", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      const client = makeClient();
      (client as any).peerConnection = {
        getStats: vi.fn(async () => {
          throw new Error("stats failed");
        }),
      };
      expect(await client.getStats()).toBeNull();
    });
  });

  // ===========================================================================
  // Events
  // ===========================================================================
  describe("events", () => {
    it("emits stateChange on setState", () => {
      const client = makeClient();
      const handler = vi.fn();
      client.on("stateChange", handler);

      (client as any).setState("connecting");
      expect(handler).toHaveBeenCalledWith({
        state: "connecting",
        previousState: "disconnected",
      });
    });

    it("emits error on logError", () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      const client = makeClient();
      const handler = vi.fn();
      client.on("error", handler);

      (client as any).logError("test error", new Error("boom"));
      expect(handler).toHaveBeenCalledWith({
        message: "test error",
        error: expect.any(Error),
      });
    });
  });

  // ===========================================================================
  // disconnect / destroy
  // ===========================================================================
  describe("disconnect", () => {
    it("sends DELETE to resource URL on disconnect", async () => {
      const fetchFn = vi.fn(async () => ({ ok: true }));
      (globalThis as any).fetch = fetchFn;

      const client = makeClient();
      (client as any).resourceUrl = "https://whip.example.com/resource/123";
      (client as any).peerConnection = { close: vi.fn() };

      await client.disconnect();
      expect(fetchFn).toHaveBeenCalledWith("https://whip.example.com/resource/123", {
        method: "DELETE",
      });
      expect(client.getState()).toBe("disconnected");
    });

    it("disconnect succeeds even if DELETE fails", async () => {
      (globalThis as any).fetch = vi.fn(async () => {
        throw new Error("network error");
      });

      const client = makeClient();
      (client as any).resourceUrl = "https://whip.example.com/resource/123";
      (client as any).peerConnection = { close: vi.fn() };

      await expect(client.disconnect()).resolves.toBeUndefined();
      expect(client.getState()).toBe("disconnected");
    });

    it("disconnect without resource URL skips DELETE", async () => {
      const fetchFn = vi.fn();
      (globalThis as any).fetch = fetchFn;

      const client = makeClient();
      (client as any).peerConnection = { close: vi.fn() };

      await client.disconnect();
      expect(fetchFn).not.toHaveBeenCalled();
    });
  });

  describe("destroy", () => {
    it("cleans up and removes listeners", () => {
      const client = makeClient();
      const handler = vi.fn();
      client.on("stateChange", handler);

      (client as any).peerConnection = { close: vi.fn() };
      client.destroy();

      expect(client.getPeerConnection()).toBeNull();
    });
  });
});
