import { describe, expect, it } from "vitest";

import {
  getAudioConstraints,
  getVideoConstraints,
  getAvailableProfiles,
  buildMediaConstraints,
  mergeWithCustomConstraints,
  getEncoderSettings,
} from "../src/core/MediaConstraints";
import type { QualityProfile } from "../src/types";

describe("MediaConstraints", () => {
  // =========================================================================
  // getAudioConstraints
  // =========================================================================
  describe("getAudioConstraints", () => {
    it.each([
      [
        "professional",
        {
          echoCancellation: false,
          noiseSuppression: false,
          autoGainControl: false,
          sampleRate: 48000,
          channelCount: 2,
          latency: 0.01,
        },
      ],
      [
        "broadcast",
        {
          echoCancellation: false,
          noiseSuppression: true,
          autoGainControl: false,
          sampleRate: 48000,
          channelCount: 2,
          latency: 0.02,
        },
      ],
      [
        "conference",
        {
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
          sampleRate: 44100,
          channelCount: 1,
          latency: 0.05,
        },
      ],
      [
        "auto",
        {
          echoCancellation: false,
          noiseSuppression: false,
          autoGainControl: false,
          sampleRate: 48000,
          channelCount: 2,
        },
      ],
    ] as const)("%s profile returns correct audio constraints", (profile, expected) => {
      const result = getAudioConstraints(profile);
      expect(result).toEqual(expected);
    });

    it("defaults to professional when called with no args", () => {
      const result = getAudioConstraints();
      expect(result.echoCancellation).toBe(false);
      expect(result.sampleRate).toBe(48000);
      expect(result.channelCount).toBe(2);
    });

    it("conference is mono with all processing enabled", () => {
      const c = getAudioConstraints("conference");
      expect(c.channelCount).toBe(1);
      expect(c.echoCancellation).toBe(true);
      expect(c.noiseSuppression).toBe(true);
      expect(c.autoGainControl).toBe(true);
    });

    it("auto profile has no latency setting", () => {
      const result = getAudioConstraints("auto");
      expect(result.latency).toBeUndefined();
    });
  });

  // =========================================================================
  // getVideoConstraints
  // =========================================================================
  describe("getVideoConstraints", () => {
    it.each([
      ["professional", 1920, 1080, 30],
      ["broadcast", 1920, 1080, 30],
      ["conference", 1280, 720, 24],
      ["auto", 1920, 1080, 30],
    ] as const)("%s: %dx%d@%dfps", (profile, width, height, fps) => {
      const result = getVideoConstraints(profile);
      expect(result.width.ideal).toBe(width);
      expect(result.height.ideal).toBe(height);
      expect(result.frameRate.ideal).toBe(fps);
    });

    it("defaults to professional", () => {
      const result = getVideoConstraints();
      expect(result.width.ideal).toBe(1920);
    });
  });

  // =========================================================================
  // getAvailableProfiles
  // =========================================================================
  describe("getAvailableProfiles", () => {
    it("returns 4 profiles", () => {
      const profiles = getAvailableProfiles();
      expect(profiles).toHaveLength(4);
    });

    it("each profile has id, name, description", () => {
      for (const p of getAvailableProfiles()) {
        expect(typeof p.id).toBe("string");
        expect(typeof p.name).toBe("string");
        expect(typeof p.description).toBe("string");
        expect(p.description.length).toBeGreaterThan(10);
      }
    });

    it("ids are valid QualityProfile values", () => {
      const ids = getAvailableProfiles().map((p) => p.id);
      expect(ids).toContain("professional");
      expect(ids).toContain("broadcast");
      expect(ids).toContain("conference");
      expect(ids).toContain("auto");
    });
  });

  // =========================================================================
  // buildMediaConstraints
  // =========================================================================
  describe("buildMediaConstraints", () => {
    it("builds constraints from profile alone", () => {
      const c = buildMediaConstraints("professional");
      expect(c.audio).toBeDefined();
      expect(c.video).toBeDefined();

      const audio = c.audio as Record<string, unknown>;
      expect(audio.echoCancellation).toBe(false);
      expect(audio.sampleRate).toBe(48000);

      const video = c.video as Record<string, unknown>;
      expect(video.width).toEqual({ ideal: 1920 });
    });

    it("includes videoDeviceId when provided", () => {
      const c = buildMediaConstraints("broadcast", { videoDeviceId: "cam-1" });
      const video = c.video as Record<string, unknown>;
      expect(video.deviceId).toEqual({ exact: "cam-1" });
    });

    it("includes audioDeviceId when provided", () => {
      const c = buildMediaConstraints("broadcast", { audioDeviceId: "mic-1" });
      const audio = c.audio as Record<string, unknown>;
      expect(audio.deviceId).toEqual({ exact: "mic-1" });
    });

    it("includes facingMode when provided", () => {
      const c = buildMediaConstraints("conference", { facingMode: "environment" });
      const video = c.video as Record<string, unknown>;
      expect(video.facingMode).toBe("environment");
    });

    it("omits deviceId/facingMode when not provided", () => {
      const c = buildMediaConstraints("professional");
      const video = c.video as Record<string, unknown>;
      const audio = c.audio as Record<string, unknown>;
      expect(video.deviceId).toBeUndefined();
      expect(video.facingMode).toBeUndefined();
      expect(audio.deviceId).toBeUndefined();
    });
  });

  // =========================================================================
  // mergeWithCustomConstraints
  // =========================================================================
  describe("mergeWithCustomConstraints", () => {
    it("returns base constraints when no custom provided", () => {
      const merged = mergeWithCustomConstraints("professional");
      const base = buildMediaConstraints("professional");
      expect(merged).toEqual(base);
    });

    it("merges custom audio overrides", () => {
      const merged = mergeWithCustomConstraints("professional", {
        audio: { sampleRate: 96000 } as MediaTrackConstraints,
      });
      const audio = merged.audio as Record<string, unknown>;
      expect(audio.sampleRate).toBe(96000);
      // Original fields preserved
      expect(audio.echoCancellation).toBe(false);
    });

    it("merges custom video overrides", () => {
      const merged = mergeWithCustomConstraints("conference", {
        video: { frameRate: { ideal: 60 } } as MediaTrackConstraints,
      });
      const video = merged.video as Record<string, unknown>;
      expect(video.frameRate).toEqual({ ideal: 60 });
      // Original width preserved
      expect(video.width).toEqual({ ideal: 1280 });
    });

    it("replaces audio entirely when custom audio is boolean", () => {
      const merged = mergeWithCustomConstraints("professional", { audio: false });
      expect(merged.audio).toBe(false);
    });

    it("replaces video entirely when custom video is boolean", () => {
      const merged = mergeWithCustomConstraints("professional", { video: false });
      expect(merged.video).toBe(false);
    });
  });

  // =========================================================================
  // getEncoderSettings
  // =========================================================================
  describe("getEncoderSettings", () => {
    it.each([
      ["professional", 4_000_000, 1920, 1080, 30],
      ["broadcast", 2_500_000, 1920, 1080, 30],
      ["conference", 1_500_000, 1280, 720, 24],
      ["auto", 2_000_000, 1920, 1080, 30],
    ] as const)("%s: video %d bps, %dx%d@%dfps", (profile, bitrate, w, h, fps) => {
      const s = getEncoderSettings(profile);
      expect(s.video.bitrate).toBe(bitrate);
      expect(s.video.width).toBe(w);
      expect(s.video.height).toBe(h);
      expect(s.video.framerate).toBe(fps);
      expect(s.video.codec).toBe("avc1.42E01E");
    });

    it("all profiles use opus audio codec", () => {
      for (const profile of ["professional", "broadcast", "conference", "auto"] as const) {
        expect(getEncoderSettings(profile).audio.codec).toBe("opus");
      }
    });

    it("all profiles use 48kHz/stereo audio encoding", () => {
      for (const profile of ["professional", "broadcast", "conference", "auto"] as const) {
        const s = getEncoderSettings(profile);
        expect(s.audio.sampleRate).toBe(48000);
        expect(s.audio.numberOfChannels).toBe(2);
        expect(s.audio.bitrate).toBe(128_000);
      }
    });

    it("defaults to professional", () => {
      const def = getEncoderSettings();
      const pro = getEncoderSettings("professional");
      expect(def).toEqual(pro);
    });
  });
});
