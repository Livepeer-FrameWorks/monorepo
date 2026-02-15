/**
 * IngestControllerHost — Lit ReactiveController wrapping IngestControllerV2.
 * Direct port of useStreamCrafterV2.ts from streamcrafter-react.
 */
import type { ReactiveController, ReactiveControllerHost } from "lit";
import {
  IngestControllerV2,
  type IngestControllerConfigV2,
  type IngestState,
  type IngestStateContextV2,
  type IngestStats,
  type CaptureOptions,
  type ScreenCaptureOptions,
  type DeviceInfo,
  type MediaSource,
  type QualityProfile,
  type ReconnectionState,
  type EncoderOverrides,
  detectCapabilities,
  isWebCodecsEncodingPathSupported,
} from "@livepeer-frameworks/streamcrafter-core";

export interface EncoderStats {
  video: {
    framesEncoded: number;
    framesPending: number;
    bytesEncoded: number;
    lastFrameTime: number;
  };
  audio: {
    samplesEncoded: number;
    samplesPending: number;
    bytesEncoded: number;
    lastSampleTime: number;
  };
  timestamp: number;
}

export interface IngestControllerHostState {
  state: IngestState;
  stateContext: IngestStateContextV2;
  isStreaming: boolean;
  isCapturing: boolean;
  isReconnecting: boolean;
  error: string | null;
  mediaStream: MediaStream | null;
  sources: MediaSource[];
  qualityProfile: QualityProfile;
  reconnectionState: ReconnectionState | null;
  stats: IngestStats | null;
  useWebCodecs: boolean;
  isWebCodecsActive: boolean;
  isWebCodecsAvailable: boolean;
  encoderStats: EncoderStats | null;
}

type HostElement = ReactiveControllerHost & HTMLElement;

export class IngestControllerHost implements ReactiveController {
  host: HostElement;
  private controller: IngestControllerV2 | null = null;
  private unsubs: Array<() => void> = [];
  private encoderStatsCleanup: (() => void) | null = null;

  s: IngestControllerHostState;

  constructor(host: HostElement, initialProfile: QualityProfile = "broadcast") {
    this.host = host;
    host.addController(this);

    const capabilities = detectCapabilities();
    this.s = {
      state: "idle",
      stateContext: {},
      isStreaming: false,
      isCapturing: false,
      isReconnecting: false,
      error: null,
      mediaStream: null,
      sources: [],
      qualityProfile: initialProfile,
      reconnectionState: null,
      stats: null,
      useWebCodecs: capabilities.recommended === "webcodecs",
      isWebCodecsActive: false,
      isWebCodecsAvailable: isWebCodecsEncodingPathSupported(),
      encoderStats: null,
    };
  }

  // ---- Configuration & Lifecycle ----

  initialize(config: IngestControllerConfigV2) {
    this.teardown();

    const controller = new IngestControllerV2({
      ...config,
      useWebCodecs: this.s.useWebCodecs,
    });
    this.controller = controller;
    this.subscribeToEvents(controller);
  }

  hostConnected() {}

  hostDisconnected() {
    this.teardown();
  }

  private teardown() {
    this.unsubs.forEach((fn) => fn());
    this.unsubs = [];
    if (this.encoderStatsCleanup) {
      this.encoderStatsCleanup();
      this.encoderStatsCleanup = null;
    }
    this.controller?.destroy();
    this.controller = null;
  }

  // ---- State Updates ----

  private update(partial: Partial<IngestControllerHostState>) {
    Object.assign(this.s, partial);
    this.host.requestUpdate();
  }

  // ---- Event Subscriptions ----

  private subscribeToEvents(controller: IngestControllerV2) {
    const u = this.unsubs;

    u.push(
      controller.on("stateChange", (event) => {
        const state = event.state;
        const ctx = (event.context ?? {}) as IngestStateContextV2;
        this.update({
          state,
          stateContext: ctx,
          isStreaming: state === "streaming",
          isCapturing: state === "capturing" || state === "streaming",
          isReconnecting: state === "reconnecting",
          mediaStream: controller.getMediaStream(),
          sources: controller.getSources(),
          reconnectionState: ctx.reconnection ?? this.s.reconnectionState,
        });
        this.dispatchEvent("fw-sc-state-change", { state, context: ctx });
      })
    );

    u.push(
      controller.on("statsUpdate", (newStats) => {
        this.update({ stats: newStats });
      })
    );

    u.push(
      controller.on("error", (event) => {
        this.update({ error: event.error });
        this.dispatchEvent("fw-sc-error", { error: event.error });
      })
    );

    u.push(
      controller.on("sourceAdded", () => {
        this.update({
          sources: controller.getSources(),
          mediaStream: controller.getMediaStream(),
        });
      })
    );

    u.push(
      controller.on("sourceRemoved", () => {
        this.update({
          sources: controller.getSources(),
          mediaStream: controller.getMediaStream(),
        });
      })
    );

    u.push(
      controller.on("sourceUpdated", () => {
        this.update({
          sources: controller.getSources(),
          mediaStream: controller.getMediaStream(),
        });
      })
    );

    u.push(
      controller.on("qualityChanged", (event) => {
        this.update({ qualityProfile: event.profile });
      })
    );

    u.push(
      controller.on("reconnectionAttempt", () => {
        this.update({
          reconnectionState: controller.getReconnectionManager().getState(),
        });
      })
    );

    u.push(
      controller.on("webCodecsActive", (event: { active: boolean }) => {
        this.update({ isWebCodecsActive: event.active });
        if (event.active) {
          this.setupEncoderStatsListener();
        }
      })
    );

    // Monitor encoder status on state changes
    u.push(
      controller.on("stateChange", (event) => {
        if (event.state === "streaming") {
          setTimeout(() => {
            this.update({ isWebCodecsActive: controller.isWebCodecsActive() });
            if (controller.isWebCodecsActive() && !this.encoderStatsCleanup) {
              this.setupEncoderStatsListener();
            }
          }, 200);
        } else if (event.state === "idle" || event.state === "capturing") {
          this.update({ isWebCodecsActive: false, encoderStats: null });
          if (this.encoderStatsCleanup) {
            this.encoderStatsCleanup();
            this.encoderStatsCleanup = null;
          }
        }
      })
    );
  }

  private setupEncoderStatsListener() {
    if (!this.controller) return;
    const encoder = this.controller.getEncoderManager();
    if (encoder) {
      this.encoderStatsCleanup = encoder.on("stats", (newStats) => {
        this.update({ encoderStats: newStats as EncoderStats });
      });
    }
  }

  private dispatchEvent(name: string, detail: unknown) {
    this.host.dispatchEvent(new CustomEvent(name, { detail, bubbles: true, composed: true }));
  }

  // ---- Capture Actions ----

  async startCamera(options?: CaptureOptions): Promise<MediaSource> {
    if (!this.controller) throw new Error("Controller not initialized");
    this.update({ error: null });
    return this.controller.startCamera(options);
  }

  async startScreenShare(options?: ScreenCaptureOptions): Promise<MediaSource | null> {
    if (!this.controller) throw new Error("Controller not initialized");
    this.update({ error: null });
    return this.controller.startScreenShare(options);
  }

  addCustomSource(stream: MediaStream, label: string): MediaSource {
    if (!this.controller) throw new Error("Controller not initialized");
    return this.controller.addCustomSource(stream, label);
  }

  removeSource(sourceId: string) {
    this.controller?.removeSource(sourceId);
  }

  async stopCapture() {
    await this.controller?.stopCapture();
  }

  // ---- Source Management ----

  setSourceVolume(sourceId: string, volume: number) {
    this.controller?.setSourceVolume(sourceId, volume);
  }

  setSourceMuted(sourceId: string, muted: boolean) {
    this.controller?.setSourceMuted(sourceId, muted);
  }

  setSourceActive(sourceId: string, active: boolean) {
    this.controller?.setSourceActive(sourceId, active);
  }

  setPrimaryVideoSource(sourceId: string) {
    this.controller?.setPrimaryVideoSource(sourceId);
  }

  // ---- Master Audio ----

  setMasterVolume(volume: number) {
    this.controller?.setMasterVolume(volume);
  }

  getMasterVolume(): number {
    return this.controller?.getMasterVolume() ?? 1;
  }

  // ---- Quality ----

  async setQualityProfile(profile: QualityProfile) {
    await this.controller?.setQualityProfile(profile);
  }

  // ---- Streaming ----

  async startStreaming() {
    if (!this.controller) throw new Error("Controller not initialized");
    this.update({ error: null });
    await this.controller.startStreaming();
  }

  async stopStreaming() {
    await this.controller?.stopStreaming();
  }

  // ---- Devices ----

  async getDevices(): Promise<DeviceInfo[]> {
    return this.controller?.getDevices() ?? [];
  }

  async switchVideoDevice(deviceId: string) {
    await this.controller?.switchVideoDevice(deviceId);
  }

  async switchAudioDevice(deviceId: string) {
    await this.controller?.switchAudioDevice(deviceId);
  }

  // ---- Stats ----

  async getStats(): Promise<IngestStats | null> {
    return this.controller?.getStats() ?? null;
  }

  // ---- Encoder ----

  setUseWebCodecs(enabled: boolean) {
    this.update({ useWebCodecs: enabled });
    this.controller?.setUseWebCodecs(enabled);
  }

  setEncoderOverrides(overrides: EncoderOverrides) {
    this.controller?.setEncoderOverrides(overrides);
  }

  // ---- Controller Access ----

  getController(): IngestControllerV2 | null {
    return this.controller;
  }
}
