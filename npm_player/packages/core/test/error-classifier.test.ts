import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { ErrorClassifier, type RecoveryAction } from "../src/core/ErrorClassifier";
import { ErrorCode, ErrorSeverity } from "../src/core/PlayerInterface";

describe("ErrorClassifier", () => {
  let classifier: ErrorClassifier;

  beforeEach(() => {
    classifier = new ErrorClassifier();
    vi.spyOn(Date, "now").mockReturnValue(100_000);
    vi.spyOn(Math, "random").mockReturnValue(0.5); // deterministic jitter
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // =========================================================================
  // mapErrorToCode — keyword→code mapping
  // =========================================================================
  describe("mapErrorToCode", () => {
    it.each([
      ["Connection timeout", ErrorCode.NETWORK_TIMEOUT],
      ["Request timed out", ErrorCode.NETWORK_TIMEOUT],
      ["fetch failed", ErrorCode.NETWORK_TIMEOUT],
      ["network error", ErrorCode.NETWORK_TIMEOUT],
      ["WebSocket closed", ErrorCode.WEBSOCKET_DISCONNECT],
      ["socket error", ErrorCode.WEBSOCKET_DISCONNECT],
      ["stream offline", ErrorCode.STREAM_OFFLINE],
      ["not found", ErrorCode.STREAM_OFFLINE],
      ["stream not found", ErrorCode.STREAM_OFFLINE],
      ["segment load failed", ErrorCode.SEGMENT_LOAD_ERROR],
      ["manifest parse error", ErrorCode.MANIFEST_STALE],
      ["playlist error", ErrorCode.MANIFEST_STALE],
      ["manifest 404", ErrorCode.STREAM_OFFLINE],
      ["ICE disconnect", ErrorCode.ICE_DISCONNECTED],
      ["ICE failure", ErrorCode.ICE_FAILED],
      ["codec error", ErrorCode.CODEC_DECODE_ERROR],
      ["decode failed", ErrorCode.CODEC_DECODE_ERROR],
      ["not supported", ErrorCode.PROTOCOL_UNSUPPORTED],
      ["unsupported format", ErrorCode.PROTOCOL_UNSUPPORTED],
      ["buffer underrun", ErrorCode.BUFFER_UNDERRUN],
      ["buffer stalled", ErrorCode.BUFFER_UNDERRUN],
      ["401 unauthorized", ErrorCode.AUTH_REQUIRED],
      ["auth token expired", ErrorCode.AUTH_REQUIRED],
      ["unauthorized access", ErrorCode.AUTH_REQUIRED],
      ["403 forbidden", ErrorCode.GEO_BLOCKED],
      ["geo restricted", ErrorCode.GEO_BLOCKED],
      ["DRM license error", ErrorCode.DRM_ERROR],
      ["EME key error", ErrorCode.DRM_ERROR],
      ["key system error", ErrorCode.DRM_ERROR],
    ])('"%s" → %s', (message, expected) => {
      expect(ErrorClassifier.mapErrorToCode(message)).toBe(expected);
    });

    it("case insensitive", () => {
      expect(ErrorClassifier.mapErrorToCode("TIMEOUT ERROR")).toBe(ErrorCode.NETWORK_TIMEOUT);
      expect(ErrorClassifier.mapErrorToCode("WebSocket Disconnect")).toBe(
        ErrorCode.WEBSOCKET_DISCONNECT
      );
    });

    it("accepts Error objects", () => {
      expect(ErrorClassifier.mapErrorToCode(new Error("connection timeout"))).toBe(
        ErrorCode.NETWORK_TIMEOUT
      );
    });

    it("returns UNKNOWN for unrecognized messages", () => {
      expect(ErrorClassifier.mapErrorToCode("something weird")).toBe(ErrorCode.UNKNOWN);
    });

    it("offline check has higher priority than segment", () => {
      // "not found" should be STREAM_OFFLINE, not SEGMENT_LOAD_ERROR
      expect(ErrorClassifier.mapErrorToCode("resource not found")).toBe(ErrorCode.STREAM_OFFLINE);
    });
  });

  // =========================================================================
  // classify — Tier 1: Transient retry
  // =========================================================================
  describe("Tier 1 — transient retry", () => {
    it("first attempt returns retry with backoff", () => {
      const action = classifier.classify(ErrorCode.NETWORK_TIMEOUT);
      expect(action.type).toBe("retry");
      if (action.type === "retry") {
        // baseDelay=500, 2^0=1, jitter 20% at random=0.5 → 500 + 500*0.2*(0.5*2-1)=500+0=500
        expect(action.delayMs).toBe(500);
      }
    });

    it("second attempt has higher backoff", () => {
      classifier.classify(ErrorCode.NETWORK_TIMEOUT); // attempt 1
      const action = classifier.classify(ErrorCode.NETWORK_TIMEOUT); // attempt 2
      expect(action.type).toBe("retry");
      if (action.type === "retry") {
        // baseDelay=500, 2^1=2 → 1000ms base, capped at 4000
        expect(action.delayMs).toBe(1000);
      }
    });

    it("exhausts retries then tries swap (with alternatives)", () => {
      classifier.setAlternativesRemaining(2);

      // NETWORK_TIMEOUT has maxAttempts=3
      classifier.classify(ErrorCode.NETWORK_TIMEOUT); // attempt 1 → retry
      classifier.classify(ErrorCode.NETWORK_TIMEOUT); // attempt 2 → retry
      const action = classifier.classify(ErrorCode.NETWORK_TIMEOUT); // attempt 3, retriesRemaining=0
      expect(action.type).toBe("swap");
    });

    it("exhausts retries then fatal (no alternatives)", () => {
      // No alternatives remaining (default)
      classifier.classify(ErrorCode.NETWORK_TIMEOUT);
      classifier.classify(ErrorCode.NETWORK_TIMEOUT);
      const action = classifier.classify(ErrorCode.NETWORK_TIMEOUT);
      expect(action.type).toBe("fatal");
      if (action.type === "fatal") {
        expect(action.error.code).toBe(ErrorCode.ALL_PROTOCOLS_EXHAUSTED);
      }
    });

    it("emits recoveryAttempted for each retry", () => {
      const handler = vi.fn();
      classifier.on("recoveryAttempted", handler);

      classifier.classify(ErrorCode.NETWORK_TIMEOUT);
      expect(handler).toHaveBeenCalledWith({
        code: ErrorCode.NETWORK_TIMEOUT,
        attempt: 1,
        maxAttempts: 3,
      });
    });

    it("WEBSOCKET_DISCONNECT has 5 max attempts", () => {
      classifier.setAlternativesRemaining(1);

      for (let i = 0; i < 4; i++) {
        expect(classifier.classify(ErrorCode.WEBSOCKET_DISCONNECT).type).toBe("retry");
      }
      // 5th attempt → retries exhausted → swap
      expect(classifier.classify(ErrorCode.WEBSOCKET_DISCONNECT).type).toBe("swap");
    });
  });

  // =========================================================================
  // classify — Tier 2: Recoverable swap
  // =========================================================================
  describe("Tier 2 — recoverable swap", () => {
    it("swaps when alternatives available", () => {
      classifier.setAlternativesRemaining(3);
      const action = classifier.classify(ErrorCode.PROTOCOL_UNSUPPORTED);
      expect(action.type).toBe("swap");
      if (action.type === "swap") {
        expect(action.reason).toBe("Protocol not supported");
      }
    });

    it("fatal when no alternatives", () => {
      const action = classifier.classify(ErrorCode.ICE_FAILED);
      expect(action.type).toBe("fatal");
      if (action.type === "fatal") {
        expect(action.error.code).toBe(ErrorCode.ALL_PROTOCOLS_EXHAUSTED);
      }
    });

    it("ICE_FAILED, CODEC_INCOMPATIBLE, MANIFEST_STALE, PLAYER_INIT_FAILED are all Tier 2", () => {
      const tier2Codes = [
        ErrorCode.CODEC_INCOMPATIBLE,
        ErrorCode.ICE_FAILED,
        ErrorCode.MANIFEST_STALE,
        ErrorCode.PLAYER_INIT_FAILED,
      ];
      for (const code of tier2Codes) {
        const c = new ErrorClassifier({ alternativesCount: 5 });
        expect(c.classify(code).type).toBe("swap");
      }
    });
  });

  // =========================================================================
  // classify — Tier 3: Degraded quality toast
  // =========================================================================
  describe("Tier 3 — degraded quality toast", () => {
    it("emits toast with quality message", () => {
      const action = classifier.classify(ErrorCode.QUALITY_DROPPED);
      expect(action.type).toBe("toast");
      if (action.type === "toast") {
        expect(action.message).toBe("Quality reduced");
      }
    });

    it("emits qualityChanged event", () => {
      const handler = vi.fn();
      classifier.on("qualityChanged", handler);

      classifier.classify(ErrorCode.QUALITY_DROPPED);
      expect(handler).toHaveBeenCalledWith({
        direction: "down",
        reason: "Quality reduced",
      });
    });

    it("debounces repeated quality toasts within 10s", () => {
      const handler = vi.fn();
      classifier.on("qualityChanged", handler);

      classifier.classify(ErrorCode.QUALITY_DROPPED);
      expect(handler).toHaveBeenCalledTimes(1);

      // Still within debounce window
      vi.spyOn(Date, "now").mockReturnValue(105_000); // 5s later
      const action2 = classifier.classify(ErrorCode.QUALITY_DROPPED);
      expect(action2.type).toBe("toast");
      if (action2.type === "toast") {
        expect(action2.message).toBe(""); // debounced
      }
      expect(handler).toHaveBeenCalledTimes(1); // not called again
    });

    it("shows toast again after debounce window expires", () => {
      const handler = vi.fn();
      classifier.on("qualityChanged", handler);

      classifier.classify(ErrorCode.QUALITY_DROPPED);

      // Past debounce window
      vi.spyOn(Date, "now").mockReturnValue(115_000); // 15s later
      const action2 = classifier.classify(ErrorCode.QUALITY_DROPPED);
      expect(action2.type).toBe("toast");
      if (action2.type === "toast") {
        expect(action2.message).toBe("Quality reduced");
      }
      expect(handler).toHaveBeenCalledTimes(2);
    });

    it("BANDWIDTH_LIMITED also triggers quality toast", () => {
      const action = classifier.classify(ErrorCode.BANDWIDTH_LIMITED);
      expect(action.type).toBe("toast");
      if (action.type === "toast") {
        expect(action.message).toBe("Bandwidth limited");
      }
    });
  });

  // =========================================================================
  // classify — Tier 4: Fatal
  // =========================================================================
  describe("Tier 4 — fatal", () => {
    it.each([
      [ErrorCode.STREAM_OFFLINE, "Stream is offline"],
      [ErrorCode.AUTH_REQUIRED, "Sign in to watch"],
      [ErrorCode.GEO_BLOCKED, "Not available in your region"],
      [ErrorCode.DRM_ERROR, "Playback not supported"],
      [ErrorCode.CONTENT_UNAVAILABLE, "Content unavailable"],
      [ErrorCode.UNKNOWN, "Playback error"],
      [ErrorCode.ALL_PROTOCOLS_EXHAUSTED, "Unable to play video"],
      [ErrorCode.ALL_PROTOCOLS_BLACKLISTED, "No compatible playback protocols available"],
    ])("%s → fatal with message '%s'", (code, expectedMessage) => {
      const action = classifier.classify(code);
      expect(action.type).toBe("fatal");
      if (action.type === "fatal") {
        expect(action.error.message).toBe(expectedMessage);
        expect(action.error.severity).toBe(ErrorSeverity.FATAL);
      }
    });

    it("emits playbackFailed event", () => {
      const handler = vi.fn();
      classifier.on("playbackFailed", handler);

      classifier.classify(ErrorCode.STREAM_OFFLINE);
      expect(handler).toHaveBeenCalledTimes(1);
      expect(handler).toHaveBeenCalledWith(
        expect.objectContaining({
          code: ErrorCode.STREAM_OFFLINE,
          severity: ErrorSeverity.FATAL,
        })
      );
    });

    it("includes originalError when provided", () => {
      const err = new Error("original");
      const action = classifier.classify(ErrorCode.STREAM_OFFLINE, err);
      if (action.type === "fatal") {
        expect(action.error.originalError).toBe(err);
      }
    });
  });

  // =========================================================================
  // calculateBackoff — exponential + jitter
  // =========================================================================
  describe("backoff calculation", () => {
    it("exponential growth per attempt", () => {
      vi.spyOn(Math, "random").mockReturnValue(0.5); // 0 jitter
      // SEGMENT_LOAD_ERROR: base=200, max=2000, jitter=10%
      const a1 = classifier.classify(ErrorCode.SEGMENT_LOAD_ERROR); // attempt 1: 200 * 2^0 = 200
      const a2 = classifier.classify(ErrorCode.SEGMENT_LOAD_ERROR); // attempt 2: 200 * 2^1 = 400

      expect(a1.type).toBe("retry");
      expect(a2.type).toBe("retry");
      if (a1.type === "retry" && a2.type === "retry") {
        expect(a2.delayMs).toBeGreaterThan(a1.delayMs);
      }
    });

    it("caps at maxDelay", () => {
      vi.spyOn(Math, "random").mockReturnValue(0.5); // 0 jitter
      // SEGMENT_LOAD_ERROR: base=200, max=2000
      // With many attempts the exponential would exceed max
      // But SEGMENT has only 3 retries, so let's test WEBSOCKET_DISCONNECT: base=500, max=5000
      const delays: number[] = [];
      for (let i = 0; i < 4; i++) {
        const action = classifier.classify(ErrorCode.WEBSOCKET_DISCONNECT);
        if (action.type === "retry") delays.push(action.delayMs);
      }
      // All should be <= 5000
      for (const d of delays) {
        expect(d).toBeLessThanOrEqual(5000);
      }
    });

    it("jitter varies with Math.random", () => {
      vi.spyOn(Math, "random").mockReturnValue(0); // min jitter
      const c1 = new ErrorClassifier();
      const a1 = c1.classify(ErrorCode.NETWORK_TIMEOUT);

      vi.spyOn(Math, "random").mockReturnValue(1); // max jitter
      const c2 = new ErrorClassifier();
      const a2 = c2.classify(ErrorCode.NETWORK_TIMEOUT);

      if (a1.type === "retry" && a2.type === "retry") {
        expect(a1.delayMs).not.toBe(a2.delayMs);
      }
    });
  });

  // =========================================================================
  // reset
  // =========================================================================
  describe("reset", () => {
    it("clears retry counts", () => {
      classifier.setAlternativesRemaining(5);
      // Exhaust some retries
      classifier.classify(ErrorCode.NETWORK_TIMEOUT); // attempt 1
      classifier.classify(ErrorCode.NETWORK_TIMEOUT); // attempt 2

      classifier.reset();

      // After reset, first attempt again
      const action = classifier.classify(ErrorCode.NETWORK_TIMEOUT);
      expect(action.type).toBe("retry");
    });

    it("clears quality toast debounce", () => {
      const handler = vi.fn();
      classifier.on("qualityChanged", handler);

      classifier.classify(ErrorCode.QUALITY_DROPPED);
      expect(handler).toHaveBeenCalledTimes(1);

      classifier.reset();

      // Should fire immediately after reset (debounce cleared)
      classifier.classify(ErrorCode.QUALITY_DROPPED);
      expect(handler).toHaveBeenCalledTimes(2);
    });
  });

  // =========================================================================
  // setAlternativesRemaining
  // =========================================================================
  describe("setAlternativesRemaining", () => {
    it("controls swap vs fatal for exhausted retries", () => {
      classifier.classify(ErrorCode.NETWORK_TIMEOUT);
      classifier.classify(ErrorCode.NETWORK_TIMEOUT);

      classifier.setAlternativesRemaining(1);
      const swap = classifier.classify(ErrorCode.NETWORK_TIMEOUT);
      expect(swap.type).toBe("swap");
    });
  });

  // =========================================================================
  // classifyWithDetails
  // =========================================================================
  describe("classifyWithDetails", () => {
    it("fatal codes return fatal directly with custom message", () => {
      const action = classifier.classifyWithDetails(
        ErrorCode.STREAM_OFFLINE,
        "Custom stream offline message",
        { incompatibilityReasons: ["no tracks"] }
      );
      expect(action.type).toBe("fatal");
      if (action.type === "fatal") {
        expect(action.error.message).toBe("Custom stream offline message");
        expect(action.error.details?.incompatibilityReasons).toEqual(["no tracks"]);
      }
    });

    it("non-fatal codes delegate to classify", () => {
      const action = classifier.classifyWithDetails(ErrorCode.NETWORK_TIMEOUT, "custom message");
      expect(action.type).toBe("retry");
    });
  });

  // =========================================================================
  // notifyProtocolSwap
  // =========================================================================
  describe("notifyProtocolSwap", () => {
    it("emits protocolSwapped event", () => {
      const handler = vi.fn();
      classifier.on("protocolSwapped", handler);

      classifier.notifyProtocolSwap("hlsjs", "native", "hls", "whep", "ICE failed");
      expect(handler).toHaveBeenCalledWith({
        fromPlayer: "hlsjs",
        toPlayer: "native",
        fromProtocol: "hls",
        toProtocol: "whep",
        reason: "ICE failed",
      });
    });
  });

  // =========================================================================
  // Event emitter — on/off
  // =========================================================================
  describe("event emitter", () => {
    it("off removes listener", () => {
      const handler = vi.fn();
      classifier.on("playbackFailed", handler);
      classifier.off("playbackFailed", handler);

      classifier.classify(ErrorCode.STREAM_OFFLINE);
      expect(handler).not.toHaveBeenCalled();
    });

    it("handler exceptions don't break emit", () => {
      const bad = vi.fn(() => {
        throw new Error("boom");
      });
      const good = vi.fn();

      classifier.on("playbackFailed", bad);
      classifier.on("playbackFailed", good);

      classifier.classify(ErrorCode.STREAM_OFFLINE);
      expect(bad).toHaveBeenCalledTimes(1);
      expect(good).toHaveBeenCalledTimes(1);
    });
  });
});
