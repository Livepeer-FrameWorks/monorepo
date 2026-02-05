import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import {
  EncoderManager,
  createEncoderConfig,
  DEFAULT_VIDEO_SETTINGS,
  DEFAULT_AUDIO_SETTINGS,
} from "../src/core/EncoderManager";

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("EncoderManager", () => {
  beforeEach(() => {
    vi.spyOn(console, "log").mockImplementation(() => {});
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // DEFAULT_VIDEO_SETTINGS
  // ===========================================================================
  describe("DEFAULT_VIDEO_SETTINGS", () => {
    it("has professional, broadcast, conference, low profiles", () => {
      expect(DEFAULT_VIDEO_SETTINGS).toHaveProperty("professional");
      expect(DEFAULT_VIDEO_SETTINGS).toHaveProperty("broadcast");
      expect(DEFAULT_VIDEO_SETTINGS).toHaveProperty("conference");
      expect(DEFAULT_VIDEO_SETTINGS).toHaveProperty("low");
    });

    it("professional is 1080p at 8Mbps", () => {
      const p = DEFAULT_VIDEO_SETTINGS.professional;
      expect(p.width).toBe(1920);
      expect(p.height).toBe(1080);
      expect(p.bitrate).toBe(8_000_000);
      expect(p.framerate).toBe(30);
    });

    it("broadcast is 1080p at 4.5Mbps", () => {
      const b = DEFAULT_VIDEO_SETTINGS.broadcast;
      expect(b.width).toBe(1920);
      expect(b.height).toBe(1080);
      expect(b.bitrate).toBe(4_500_000);
    });

    it("conference is 720p at 2.5Mbps", () => {
      const c = DEFAULT_VIDEO_SETTINGS.conference;
      expect(c.width).toBe(1280);
      expect(c.height).toBe(720);
      expect(c.bitrate).toBe(2_500_000);
    });

    it("low is 480p at 1Mbps", () => {
      const l = DEFAULT_VIDEO_SETTINGS.low;
      expect(l.width).toBe(640);
      expect(l.height).toBe(480);
      expect(l.bitrate).toBe(1_000_000);
    });
  });

  // ===========================================================================
  // DEFAULT_AUDIO_SETTINGS
  // ===========================================================================
  describe("DEFAULT_AUDIO_SETTINGS", () => {
    it("uses opus at 48kHz stereo 128kbps", () => {
      expect(DEFAULT_AUDIO_SETTINGS.codec).toBe("opus");
      expect(DEFAULT_AUDIO_SETTINGS.sampleRate).toBe(48000);
      expect(DEFAULT_AUDIO_SETTINGS.numberOfChannels).toBe(2);
      expect(DEFAULT_AUDIO_SETTINGS.bitrate).toBe(128_000);
    });
  });

  // ===========================================================================
  // createEncoderConfig — pure function + H264 codec selection
  // ===========================================================================
  describe("createEncoderConfig", () => {
    it("defaults to broadcast profile", () => {
      const config = createEncoderConfig();
      expect(config.video.width).toBe(1920);
      expect(config.video.height).toBe(1080);
      expect(config.video.bitrate).toBe(4_500_000);
      expect(config.audio.codec).toBe("opus");
    });

    it("professional profile", () => {
      const config = createEncoderConfig("professional");
      expect(config.video.bitrate).toBe(8_000_000);
      expect(config.video.width).toBe(1920);
      expect(config.video.height).toBe(1080);
    });

    it("conference profile", () => {
      const config = createEncoderConfig("conference");
      expect(config.video.width).toBe(1280);
      expect(config.video.height).toBe(720);
    });

    it("low profile", () => {
      const config = createEncoderConfig("low");
      expect(config.video.width).toBe(640);
      expect(config.video.height).toBe(480);
      expect(config.video.framerate).toBe(24);
    });

    it("overrides video width and height", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 1280, height: 720 },
      });
      expect(config.video.width).toBe(1280);
      expect(config.video.height).toBe(720);
    });

    it("overrides video bitrate", () => {
      const config = createEncoderConfig("broadcast", {
        video: { bitrate: 6_000_000 },
      });
      expect(config.video.bitrate).toBe(6_000_000);
    });

    it("overrides audio bitrate", () => {
      const config = createEncoderConfig("broadcast", {
        audio: { bitrate: 256_000 },
      });
      expect(config.audio.bitrate).toBe(256_000);
    });

    it("overrides audio sampleRate and channels", () => {
      const config = createEncoderConfig("broadcast", {
        audio: { sampleRate: 44100, numberOfChannels: 1 },
      });
      expect(config.audio.sampleRate).toBe(44100);
      expect(config.audio.numberOfChannels).toBe(1);
    });

    it("overrides framerate", () => {
      const config = createEncoderConfig("broadcast", {
        video: { framerate: 60 },
      });
      expect(config.video.framerate).toBe(60);
    });

    // H.264 codec level selection via createEncoderConfig
    it("selects Level 3.1 for sub-720p", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 640, height: 480, framerate: 30 },
      });
      expect(config.video.codec).toBe("avc1.64001f");
    });

    it("selects Level 4.0 for 720p30", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 1280, height: 720, framerate: 30 },
      });
      expect(config.video.codec).toBe("avc1.640028");
    });

    it("selects Level 4.2 for 720p60", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 1280, height: 720, framerate: 60 },
      });
      expect(config.video.codec).toBe("avc1.64002a");
    });

    it("selects Level 4.2 for 1080p30", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 1920, height: 1080, framerate: 30 },
      });
      expect(config.video.codec).toBe("avc1.64002a");
    });

    it("selects Level 5.0 for 1080p60", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 1920, height: 1080, framerate: 60 },
      });
      expect(config.video.codec).toBe("avc1.640032");
    });

    it("selects Level 5.0 for 1440p30", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 2560, height: 1440, framerate: 30 },
      });
      expect(config.video.codec).toBe("avc1.640032");
    });

    it("selects Level 5.1 for 1440p60", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 2560, height: 1440, framerate: 60 },
      });
      expect(config.video.codec).toBe("avc1.640033");
    });

    it("selects Level 5.1 for 4K30", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 3840, height: 2160, framerate: 30 },
      });
      expect(config.video.codec).toBe("avc1.640033");
    });

    it("selects Level 5.2 for 4K60", () => {
      const config = createEncoderConfig("broadcast", {
        video: { width: 3840, height: 2160, framerate: 60 },
      });
      expect(config.video.codec).toBe("avc1.640034");
    });
  });

  // ===========================================================================
  // Constructor and state accessors
  // ===========================================================================
  describe("constructor and state", () => {
    it("starts not initialized and not running", () => {
      const em = new EncoderManager();
      expect(em.getIsInitialized()).toBe(false);
      expect(em.getIsRunning()).toBe(false);
    });

    it("getStats returns null before init", () => {
      const em = new EncoderManager();
      expect(em.getStats()).toBeNull();
    });

    it("getConfig returns null before init", () => {
      const em = new EncoderManager();
      expect(em.getConfig()).toBeNull();
    });
  });

  // ===========================================================================
  // start/updateConfig guards
  // ===========================================================================
  describe("pre-init guards", () => {
    it("start throws when not initialized", async () => {
      const em = new EncoderManager();
      await expect(em.start()).rejects.toThrow("not initialized");
    });

    it("updateConfig throws when not initialized", async () => {
      const em = new EncoderManager();
      await expect(em.updateConfig({ video: { bitrate: 5_000_000 } as any })).rejects.toThrow(
        "not initialized"
      );
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("can be called on fresh instance without error", () => {
      const em = new EncoderManager();
      expect(() => em.destroy()).not.toThrow();
    });

    it("sets initialized to false", () => {
      const em = new EncoderManager();
      em.destroy();
      expect(em.getIsInitialized()).toBe(false);
    });
  });

  // ===========================================================================
  // Event system inherited from TypedEventEmitter
  // ===========================================================================
  describe("event system", () => {
    it("on returns unsubscribe function", () => {
      const em = new EncoderManager();
      const handler = vi.fn();
      const unsub = em.on("ready", handler);
      expect(typeof unsub).toBe("function");
    });

    it("removeAllListeners clears handlers", () => {
      const em = new EncoderManager();
      const handler = vi.fn();
      em.on("ready", handler);
      em.removeAllListeners();
      // No way to verify directly, but shouldn't throw
    });
  });
});
