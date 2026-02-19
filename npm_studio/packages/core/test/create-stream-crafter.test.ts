import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

// Mock IngestControllerV2 before importing the facade
vi.mock("../src/core/IngestControllerV2", () => {
  return {
    IngestControllerV2: vi.fn().mockImplementation(function (this: any) {
      const handlers = new Map<string, Set<Function>>();

      this.getState = vi.fn().mockReturnValue("idle");
      this.getStateContext = vi.fn().mockReturnValue({});
      this.isStreaming = vi.fn().mockReturnValue(false);
      this.isCapturing = vi.fn().mockReturnValue(false);
      this.isReconnecting = vi.fn().mockReturnValue(false);
      this.getSources = vi.fn().mockReturnValue([]);
      this.getPrimaryVideoSource = vi.fn().mockReturnValue(null);
      this.getStats = vi.fn().mockResolvedValue(null);
      this.getDevices = vi.fn().mockResolvedValue([]);
      this.getMediaStream = vi.fn().mockReturnValue(null);
      this.isCompositorEnabled = vi.fn().mockReturnValue(false);
      this.isWebCodecsActive = vi.fn().mockReturnValue(false);
      this.getEncoderOverrides = vi.fn().mockReturnValue({});
      this.getMasterVolume = vi.fn().mockReturnValue(1);
      this.setMasterVolume = vi.fn();
      this.getQualityProfile = vi.fn().mockReturnValue("broadcast");
      this.setQualityProfile = vi.fn();
      this.getUseWebCodecs = vi.fn().mockReturnValue(false);
      this.setUseWebCodecs = vi.fn();
      this.startCamera = vi.fn().mockResolvedValue({ id: "cam-1" });
      this.startScreenShare = vi.fn().mockResolvedValue({ id: "screen-1" });
      this.addCustomSource = vi.fn().mockReturnValue({ id: "custom-1" });
      this.removeSource = vi.fn();
      this.stopCapture = vi.fn().mockResolvedValue(undefined);
      this.setSourceVolume = vi.fn();
      this.setSourceMuted = vi.fn();
      this.setSourceActive = vi.fn();
      this.setPrimaryVideoSource = vi.fn();
      this.startStreaming = vi.fn().mockResolvedValue(undefined);
      this.stopStreaming = vi.fn().mockResolvedValue(undefined);
      this.switchVideoDevice = vi.fn().mockResolvedValue(undefined);
      this.switchAudioDevice = vi.fn().mockResolvedValue(undefined);
      this.setEncoderOverrides = vi.fn();
      this.destroy = vi.fn();
      this.getSceneManager = vi.fn().mockReturnValue(null);

      // Recording
      this.startRecording = vi.fn();
      this.stopRecording = vi.fn().mockReturnValue(null);
      this.pauseRecording = vi.fn();
      this.resumeRecording = vi.fn();
      this.isRecordingActive = vi.fn().mockReturnValue(false);
      this.getRecordingDuration = vi.fn().mockReturnValue(0);
      this.getRecordingFileSize = vi.fn().mockReturnValue(0);

      // Codec + bitrate
      this.getVideoCodecFamily = vi.fn().mockReturnValue("h264");
      this.isAdaptiveBitrateActive = vi.fn().mockReturnValue(false);
      this.getCurrentBitrate = vi.fn().mockReturnValue(null);
      this.getCongestionLevel = vi.fn().mockReturnValue(null);

      // Compositor
      this.enableCompositor = vi.fn().mockResolvedValue(undefined);
      this.disableCompositor = vi.fn();

      // Event emitter
      this.on = vi.fn((event: string, handler: Function) => {
        let set = handlers.get(event);
        if (!set) {
          set = new Set();
          handlers.set(event, set);
        }
        set.add(handler);
        return () => set!.delete(handler);
      });

      this._handlers = handlers;
    }),
  };
});

// Mock FeatureDetection since it's imported transitively
vi.mock("../src/core/FeatureDetection", () => ({
  detectCapabilities: vi.fn().mockReturnValue({
    webcodecs: { videoEncoder: false },
    recommended: "mediastream",
  }),
  isRTCRtpScriptTransformSupported: vi.fn().mockReturnValue(false),
}));

import { createStreamCrafter } from "../src/vanilla/createStreamCrafter";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";

function getCtrl(): any {
  return (IngestControllerV2 as any).mock.results[
    (IngestControllerV2 as any).mock.results.length - 1
  ].value;
}

describe("createStreamCrafter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("query getters", () => {
    it("exposes state from controller", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.state).toBe("idle");
      studio.destroy();
    });

    it("exposes streaming boolean", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.streaming).toBe(false);
      const ctrl = getCtrl();
      ctrl.isStreaming.mockReturnValue(true);
      expect(studio.streaming).toBe(true);
      studio.destroy();
    });

    it("exposes sources array", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.sources).toEqual([]);
      studio.destroy();
    });

    it("exposes compositorEnabled", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.compositorEnabled).toBe(false);
      studio.destroy();
    });
  });

  describe("new query getters", () => {
    it("exposes recording state", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.recording).toBe(false);
      const ctrl = getCtrl();
      ctrl.isRecordingActive.mockReturnValue(true);
      expect(studio.recording).toBe(true);
      studio.destroy();
    });

    it("exposes recordingDuration", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.recordingDuration).toBe(0);
      const ctrl = getCtrl();
      ctrl.getRecordingDuration.mockReturnValue(5000);
      expect(studio.recordingDuration).toBe(5000);
      studio.destroy();
    });

    it("exposes recordingFileSize", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.recordingFileSize).toBe(0);
      studio.destroy();
    });

    it("exposes codecFamily", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.codecFamily).toBe("h264");
      studio.destroy();
    });

    it("exposes adaptiveBitrateActive", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.adaptiveBitrateActive).toBe(false);
      studio.destroy();
    });

    it("exposes currentBitrate", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.currentBitrate).toBeNull();
      const ctrl = getCtrl();
      ctrl.getCurrentBitrate.mockReturnValue(3_600_000);
      expect(studio.currentBitrate).toBe(3_600_000);
      studio.destroy();
    });

    it("exposes congestionLevel", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.congestionLevel).toBeNull();
      studio.destroy();
    });

    it("exposes sceneManager", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.sceneManager).toBeNull();
      studio.destroy();
    });
  });

  describe("read/write properties", () => {
    it("gets and sets masterVolume", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.masterVolume).toBe(1);
      studio.masterVolume = 0.5;
      const ctrl = getCtrl();
      expect(ctrl.setMasterVolume).toHaveBeenCalledWith(0.5);
      studio.destroy();
    });

    it("gets and sets profile", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      studio.profile = "professional";
      const ctrl = getCtrl();
      expect(ctrl.setQualityProfile).toHaveBeenCalledWith("professional");
      studio.destroy();
    });

    it("gets and sets useWebCodecs", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      studio.useWebCodecs = true;
      const ctrl = getCtrl();
      expect(ctrl.setUseWebCodecs).toHaveBeenCalledWith(true);
      studio.destroy();
    });
  });

  describe("mutation methods", () => {
    it("startCamera delegates to controller", async () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      const result = await studio.startCamera();
      const ctrl = getCtrl();
      expect(ctrl.startCamera).toHaveBeenCalled();
      expect(result).toEqual({ id: "cam-1" });
      studio.destroy();
    });

    it("goLive delegates to startStreaming", async () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      await studio.goLive();
      const ctrl = getCtrl();
      expect(ctrl.startStreaming).toHaveBeenCalled();
      studio.destroy();
    });

    it("stop delegates to stopStreaming", async () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      await studio.stop();
      const ctrl = getCtrl();
      expect(ctrl.stopStreaming).toHaveBeenCalled();
      studio.destroy();
    });
  });

  describe("recording methods", () => {
    it("startRecording delegates to controller", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      studio.startRecording();
      const ctrl = getCtrl();
      expect(ctrl.startRecording).toHaveBeenCalled();
      studio.destroy();
    });

    it("stopRecording returns blob from controller", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      const ctrl = getCtrl();
      const fakeBlob = new Blob(["data"], { type: "video/webm" });
      ctrl.stopRecording.mockReturnValue(fakeBlob);
      const result = studio.stopRecording();
      expect(result).toBe(fakeBlob);
      studio.destroy();
    });

    it("pauseRecording delegates to controller", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      studio.pauseRecording();
      const ctrl = getCtrl();
      expect(ctrl.pauseRecording).toHaveBeenCalled();
      studio.destroy();
    });

    it("resumeRecording delegates to controller", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      studio.resumeRecording();
      const ctrl = getCtrl();
      expect(ctrl.resumeRecording).toHaveBeenCalled();
      studio.destroy();
    });
  });

  describe("compositor methods", () => {
    it("enableCompositor delegates to controller", async () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      await studio.enableCompositor();
      const ctrl = getCtrl();
      expect(ctrl.enableCompositor).toHaveBeenCalled();
      studio.destroy();
    });

    it("disableCompositor delegates to controller", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      studio.disableCompositor();
      const ctrl = getCtrl();
      expect(ctrl.disableCompositor).toHaveBeenCalled();
      studio.destroy();
    });
  });

  describe("on() subscriptions", () => {
    it("forwards event subscription to controller", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      const handler = vi.fn();
      const unsub = studio.on("stateChange", handler);
      const ctrl = getCtrl();
      expect(ctrl.on).toHaveBeenCalledWith("stateChange", handler);
      expect(typeof unsub).toBe("function");
      studio.destroy();
    });

    it("unsub function removes listener", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      const handler = vi.fn();
      const unsub = studio.on("error", handler);
      unsub();
      // The mock's return value is called, which deletes the handler
      studio.destroy();
    });
  });

  describe("reactiveState", () => {
    it("exposes reactive state object", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.reactiveState).toBeDefined();
      expect(typeof studio.reactiveState.on).toBe("function");
      expect(typeof studio.reactiveState.get).toBe("function");
      expect(typeof studio.reactiveState.destroy).toBe("function");
      studio.destroy();
    });
  });

  describe("i18n", () => {
    it("t() translates with default locale", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      expect(studio.t("goLive")).toBe("Go Live");
      studio.destroy();
    });

    it("t() translates with custom locale", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip", locale: "es" });
      expect(studio.t("goLive")).toBe("Transmitir");
      studio.destroy();
    });

    it("t() applies custom translations", () => {
      const studio = createStreamCrafter({
        whipUrl: "https://x.com/whip",
        translations: { goLive: "Broadcast" },
      });
      expect(studio.t("goLive")).toBe("Broadcast");
      studio.destroy();
    });
  });

  describe("destroy", () => {
    it("destroys controller and reactive state", () => {
      const studio = createStreamCrafter({ whipUrl: "https://x.com/whip" });
      studio.destroy();
      const ctrl = getCtrl();
      expect(ctrl.destroy).toHaveBeenCalled();
    });
  });
});
