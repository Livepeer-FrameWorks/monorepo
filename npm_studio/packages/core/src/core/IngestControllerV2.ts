/**
 * Ingest Controller V2
 * Enhanced orchestrator with Phase 2 features:
 * - Multi-source support (camera + screen simultaneously)
 * - Audio mixing
 * - Quality profile switching
 * - Automatic reconnection with exponential backoff
 */

import { TypedEventEmitter } from "./EventEmitter";
import { DeviceManager } from "./DeviceManager";
import { ScreenCapture } from "./ScreenCapture";
import { WhipClient } from "./WhipClient";
import { AudioMixer } from "./AudioMixer";
import { ReconnectionManager } from "./ReconnectionManager";
import { SceneManager } from "./SceneManager";
import { EncoderManager, createMultiCodecEncoderConfig } from "./EncoderManager";
import type { VideoCodecFamily } from "./CodecProfiles";
import { RecordingManager } from "../recording/RecordingManager";
import { BitrateAdaptation } from "./BitrateAdaptation";
import type { WebMWriterOptions } from "../recording/WebMWriter";
import { detectCapabilities, isRTCRtpScriptTransformSupported } from "./FeatureDetection";
import { getVideoConstraints } from "./MediaConstraints";
import type {
  IngestControllerConfigV2,
  IngestControllerEventsV2,
  IngestState,
  IngestStateContextV2,
  IngestStats,
  CaptureOptions,
  ScreenCaptureOptions,
  QualityProfile,
  DeviceInfo,
  MediaSource,
  SourceType,
  CompositorConfig,
  EncoderOverrides,
} from "../types";
import { DEFAULT_COMPOSITOR_CONFIG } from "../types";

let sourceIdCounter = 0;
function generateSourceId(type: SourceType): string {
  return `${type}-${++sourceIdCounter}-${Date.now()}`;
}

export class IngestControllerV2 extends TypedEventEmitter<IngestControllerEventsV2> {
  private config: IngestControllerConfigV2;
  private deviceManager: DeviceManager;
  private screenCapture: ScreenCapture;
  private audioMixer: AudioMixer;
  private reconnectionManager: ReconnectionManager;
  private whipClient: WhipClient | null = null;
  private whipEndpoints: string[] = [];
  private currentEndpointIndex = 0;
  private isStoppingIntentionally = false;

  private state: IngestState = "idle";
  private stateContext: IngestStateContextV2 = {};
  private sources: Map<string, MediaSource> = new Map();
  private outputStream: MediaStream | null = null;
  private currentProfile: QualityProfile;
  private useWebCodecs: boolean;
  private statsInterval: ReturnType<typeof setInterval> | null = null;
  private lastStats: IngestStats | null = null;
  private statsInFlight = false;

  // Phase 3: Compositor
  private sceneManager: SceneManager | null = null;
  private compositorBaseConfig: CompositorConfig | null = null;

  // Phase 2.5: WebCodecs Encoder (Path C)
  private encoderManager: EncoderManager | null = null;
  private encoderOverrides: EncoderOverrides = {};
  private videoCodecFamily: VideoCodecFamily = "h264";

  // Recording
  private recordingManager: RecordingManager | null = null;

  // Adaptive bitrate
  private bitrateAdaptation: BitrateAdaptation | null = null;

  // Lifecycle: listener cleanup
  private eventForwardingUnsubs: Array<() => void> = [];
  private whipClientUnsubs: Array<() => void> = [];

  constructor(config: IngestControllerConfigV2) {
    super();
    this.config = config;
    this.currentProfile = config.profile || "broadcast";
    this.whipEndpoints = this.buildWhipEndpoints(config);
    this.deviceManager = new DeviceManager();
    this.screenCapture = new ScreenCapture();
    this.audioMixer = new AudioMixer();
    this.reconnectionManager = new ReconnectionManager(config.reconnection);

    // Determine if we should use WebCodecs
    const capabilities = detectCapabilities();
    this.useWebCodecs = config.useWebCodecs ?? capabilities.recommended === "webcodecs";

    // Set up event forwarding
    this.setupEventForwarding();

    this.log("IngestControllerV2 initialized", {
      useWebCodecs: this.useWebCodecs,
      profile: this.currentProfile,
      audioMixing: config.audioMixing ?? false,
    });
  }

  /**
   * Build WHIP endpoint list with primary first.
   */
  private buildWhipEndpoints(config: IngestControllerConfigV2): string[] {
    if (config.whipUrls && config.whipUrls.length > 0) {
      const deduped = [config.whipUrl, ...config.whipUrls].filter(
        (value, index, self) => self.indexOf(value) === index
      );
      return deduped;
    }
    return [config.whipUrl];
  }

  /**
   * Get current WHIP endpoint without rotating.
   */
  private getCurrentWhipUrl(): string {
    return this.whipEndpoints[this.currentEndpointIndex] ?? this.config.whipUrl;
  }

  /**
   * Rotate to next WHIP endpoint if available.
   */
  private getNextWhipUrl(): string {
    if (this.whipEndpoints.length > 1) {
      this.currentEndpointIndex = (this.currentEndpointIndex + 1) % this.whipEndpoints.length;
    }
    return this.getCurrentWhipUrl();
  }

  /**
   * Debug logging
   */
  private log(message: string, data?: unknown): void {
    if (this.config.debug) {
      console.log(`[IngestControllerV2] ${message}`, data ?? "");
    }
  }

  /**
   * Set up event forwarding from child components
   */
  private setupEventForwarding(): void {
    this.eventForwardingUnsubs.push(
      this.deviceManager.on("devicesChanged", (event) => {
        this.emit("deviceChange", event);
      }),

      this.deviceManager.on("error", (event) => {
        this.emit("error", { error: event.message, recoverable: true });
      }),

      this.screenCapture.on("ended", (event) => {
        this.log("Screen capture ended", event);
        if (event.stream) {
          for (const [id, source] of this.sources) {
            if (source.type === "screen" && source.stream === event.stream) {
              this.removeSource(id);
              break;
            }
          }
        }
      }),

      this.screenCapture.on("error", (event) => {
        this.emit("error", { error: event.message, recoverable: true });
      }),

      this.reconnectionManager.on("attemptStart", (event) => {
        this.emit("reconnectionAttempt", {
          attempt: event.attempt,
          maxAttempts: this.reconnectionManager.getMaxAttempts(),
        });
      }),

      this.reconnectionManager.on("attemptSuccess", () => {
        this.emit("reconnectionSuccess", undefined);
        this.setState("streaming");
      }),

      this.reconnectionManager.on("attemptFailed", (event) => {
        this.log("Reconnection attempt failed", event);
      }),

      this.reconnectionManager.on("exhausted", () => {
        this.emit("reconnectionFailed", { error: "All reconnection attempts exhausted" });
        this.setState("error", { error: "Connection lost - reconnection failed" });
      })
    );
  }

  /**
   * Update state
   */
  private setState(newState: IngestState, context?: Partial<IngestStateContextV2>): void {
    this.state = newState;
    if (context) {
      this.stateContext = { ...this.stateContext, ...context };
    }
    this.stateContext.sources = Array.from(this.sources.values());
    this.stateContext.activeProfile = this.currentProfile;
    this.stateContext.reconnection = this.reconnectionManager.getState();
    this.emit("stateChange", { state: this.state, context: this.stateContext });
  }

  /**
   * Initialize audio mixer if needed
   */
  private async ensureAudioMixer(): Promise<void> {
    if (this.config.audioMixing && this.audioMixer.getState() === null) {
      await this.audioMixer.initialize();
    }
  }

  /**
   * Add a media source
   */
  private addMediaSource(type: SourceType, stream: MediaStream, label: string): MediaSource {
    const id = generateSourceId(type);

    // Check if this is the first video source (will be primary by default)
    const hasVideoTrack = stream.getVideoTracks().length > 0;
    const existingVideoSources = Array.from(this.sources.values()).filter(
      (s) => s.stream.getVideoTracks().length > 0
    );
    const isPrimaryVideo = hasVideoTrack && existingVideoSources.length === 0;

    const source: MediaSource = {
      id,
      type,
      stream,
      label,
      active: true,
      muted: false,
      volume: 1.0,
      primaryVideo: isPrimaryVideo,
    };

    this.sources.set(id, source);
    this.log(`Added source: ${id} (${type})`, {
      label,
      tracks: stream.getTracks().length,
      primaryVideo: isPrimaryVideo,
    });

    // Add audio track to mixer if enabled
    if (this.config.audioMixing) {
      const audioTrack = stream.getAudioTracks()[0];
      if (audioTrack) {
        this.audioMixer.addSource(id, audioTrack, { volume: 1.0 });
      }
    }

    // Bind to compositor if enabled
    if (this.sceneManager && this.sceneManager.isInitialized()) {
      this.log("Binding source to compositor", { sourceId: id });
      this.sceneManager.bindSource(id, stream);
      // Add layer to active scene
      const activeScene = this.sceneManager.getActiveScene();
      this.log("Adding layer to scene", {
        sourceId: id,
        activeSceneId: activeScene?.id,
        sceneLayers: activeScene?.layers.length,
      });
      if (activeScene) {
        this.sceneManager.addLayer(activeScene.id, id);
        this.log("Layer added", { sourceId: id, layerCount: activeScene.layers.length });
      }
    } else {
      this.log("Compositor not ready when adding source", {
        sourceId: id,
        hasSceneManager: !!this.sceneManager,
        isInitialized: this.sceneManager?.isInitialized() ?? false,
      });
    }

    this.emit("sourceAdded", { source });
    this.updateOutputStreamFromSources();

    return source;
  }

  /**
   * Remove a source by ID
   */
  removeSource(id: string): void {
    const source = this.sources.get(id);
    if (!source) return;

    const wasPrimaryVideo = source.primaryVideo;

    // Stop all tracks
    source.stream.getTracks().forEach((track) => track.stop());

    // Remove from audio mixer
    if (this.config.audioMixing) {
      this.audioMixer.removeSource(id);
    }

    // Unbind from compositor
    if (this.sceneManager) {
      this.sceneManager.unbindSource(id);
      // Remove layer from active scene
      const activeScene = this.sceneManager.getActiveScene();
      if (activeScene) {
        const layer = activeScene.layers.find((l) => l.sourceId === id);
        if (layer) {
          this.sceneManager.removeLayer(activeScene.id, layer.id);
        }
      }
    }

    this.sources.delete(id);
    this.log(`Removed source: ${id}`);

    // If this was the primary video, reassign to first available video source
    if (wasPrimaryVideo) {
      const videoSources = Array.from(this.sources.values()).filter(
        (s) => s.stream.getVideoTracks().length > 0
      );
      if (videoSources.length > 0) {
        videoSources[0].primaryVideo = true;
        this.sources.set(videoSources[0].id, videoSources[0]);
        this.log(`Reassigned primary video to: ${videoSources[0].id}`);
      }
    }

    this.emit("sourceRemoved", { sourceId: id });
    this.updateOutputStreamFromSources();
  }

  /**
   * Set a source as the primary video source
   */
  setPrimaryVideoSource(sourceId: string): void {
    const source = this.sources.get(sourceId);
    if (!source) return;

    // Check if this source has video
    if (source.stream.getVideoTracks().length === 0) {
      this.log(`Cannot set source ${sourceId} as primary - no video track`);
      return;
    }

    // Clear primary from all sources
    for (const [id, s] of this.sources) {
      if (s.primaryVideo) {
        s.primaryVideo = false;
        this.sources.set(id, s);
      }
    }

    // Set new primary
    source.primaryVideo = true;
    this.sources.set(sourceId, source);

    this.log(`Set primary video source: ${sourceId}`);
    this.emit("sourceUpdated", { source, changes: { primaryVideo: true } });
    this.updateOutputStreamFromSources();
  }

  /**
   * Get the current primary video source
   */
  getPrimaryVideoSource(): MediaSource | null {
    for (const source of this.sources.values()) {
      if (source.primaryVideo) return source;
    }
    return null;
  }

  /**
   * Update output stream from all sources
   * Phase 2: Primary video + mixed audio
   * Phase 3: Compositor for multi-source composition
   */
  private updateOutputStreamFromSources(): void {
    const sourcesArray = Array.from(this.sources.values()).filter((s) => s.active);

    if (sourcesArray.length === 0) {
      this.outputStream = null;
      return;
    }

    // Create new output stream
    const tracks: MediaStreamTrack[] = [];

    // Phase 3: Use compositor when enabled
    if (this.sceneManager && this.sceneManager.isInitialized()) {
      const compositedTrack = this.sceneManager.getOutputTrack();
      if (compositedTrack) {
        tracks.push(compositedTrack);
      }
    } else {
      // Legacy path: Get video track from primary video source
      const videoSourcesWithVideo = sourcesArray.filter(
        (s) => s.stream.getVideoTracks().length > 0
      );
      const primaryVideoSource =
        videoSourcesWithVideo.find((s) => s.primaryVideo) || videoSourcesWithVideo[0];

      if (primaryVideoSource) {
        const videoTrack = primaryVideoSource.stream.getVideoTracks()[0];
        if (videoTrack) {
          tracks.push(videoTrack);
        }
      }
    }

    // Get audio (mixed or primary)
    if (this.config.audioMixing && this.audioMixer.getState() === "running") {
      const mixedAudioTrack = this.audioMixer.getOutputTrack();
      if (mixedAudioTrack) {
        tracks.push(mixedAudioTrack);
      }
    } else {
      // Use first available audio track
      for (const source of sourcesArray) {
        const audioTrack = source.stream.getAudioTracks()[0];
        if (audioTrack && !source.muted) {
          tracks.push(audioTrack);
          break;
        }
      }
    }

    this.outputStream = tracks.length > 0 ? new MediaStream(tracks) : null;

    // Update WHIP client if streaming
    if (this.whipClient && this.state === "streaming") {
      this.updateWhipTracks();
    }

    // Update EncoderManager input stream if WebCodecs is active
    if (this.encoderManager && this.outputStream) {
      this.encoderManager.updateInputStream(this.outputStream).catch((err) => {
        this.log("Failed to update encoder input stream", err);
      });
    }

    this.log("Output stream updated", {
      videoTracks: this.outputStream?.getVideoTracks().length ?? 0,
      audioTracks: this.outputStream?.getAudioTracks().length ?? 0,
      usingCompositor: !!this.sceneManager,
    });
  }

  /**
   * Update WHIP client tracks when sources change
   */
  private async updateWhipTracks(): Promise<void> {
    if (!this.whipClient || !this.outputStream) return;

    try {
      const pc = this.whipClient.getPeerConnection();
      if (!pc) return;

      const senders = pc.getSenders();

      // Update video track (replaceTrack(null) properly stops sending)
      const newVideoTrack = this.outputStream.getVideoTracks()[0];
      const videoSender = senders.find((s) => s.track?.kind === "video");
      if (videoSender) {
        await videoSender.replaceTrack(newVideoTrack ?? null);
      }

      // Update audio track (replaceTrack(null) properly stops sending)
      const newAudioTrack = this.outputStream.getAudioTracks()[0];
      const audioSender = senders.find((s) => s.track?.kind === "audio");
      if (audioSender) {
        await audioSender.replaceTrack(newAudioTrack ?? null);
      }
    } catch (error) {
      this.log("Error updating WHIP tracks", error);
    }
  }

  /**
   * Start camera capture
   */
  async startCamera(options: CaptureOptions = {}): Promise<MediaSource> {
    this.log("Starting camera capture", options);
    this.setState("requesting_permissions");

    try {
      await this.ensureAudioMixer();

      const profile = options.profile || this.currentProfile;

      // If encoder overrides are set, use them for capture constraints
      const captureOptions: CaptureOptions = { ...options, profile };

      if (this.encoderOverrides?.video) {
        const videoOverrides = this.encoderOverrides.video;
        const baseConstraints = getVideoConstraints(profile);

        // Merge encoder overrides into custom video constraints
        captureOptions.customConstraints = {
          video: {
            ...baseConstraints,
            ...(videoOverrides.width && { width: { ideal: videoOverrides.width } }),
            ...(videoOverrides.height && { height: { ideal: videoOverrides.height } }),
            ...(videoOverrides.framerate && { frameRate: { ideal: videoOverrides.framerate } }),
          },
          audio: true,
        };
        this.log(
          "Using encoder overrides for capture constraints:",
          captureOptions.customConstraints
        );
      }

      const stream = await this.deviceManager.getUserMedia(captureOptions);

      const label = await this.getCameraLabel(stream);
      const source = this.addMediaSource("camera", stream, label);

      this.setState("capturing", {
        hasVideo: stream.getVideoTracks().length > 0,
        hasAudio: stream.getAudioTracks().length > 0,
      });

      return source;
    } catch (error) {
      this.setState("error", {
        error: error instanceof Error ? error.message : String(error),
      });
      throw error;
    }
  }

  /**
   * Get camera label from stream
   */
  private async getCameraLabel(stream: MediaStream): Promise<string> {
    const videoTrack = stream.getVideoTracks()[0];
    if (videoTrack) {
      return videoTrack.label || "Camera";
    }
    return "Camera";
  }

  /**
   * Start screen share capture
   */
  async startScreenShare(options: ScreenCaptureOptions = {}): Promise<MediaSource | null> {
    this.log("Starting screen share", options);
    this.setState("requesting_permissions");

    try {
      await this.ensureAudioMixer();

      // If encoder overrides are set, use them for capture constraints
      const captureOptions: ScreenCaptureOptions = { ...options };

      if (this.encoderOverrides?.video) {
        const videoOverrides = this.encoderOverrides.video;

        // Build custom video constraints from encoder overrides
        captureOptions.video = {
          ...(videoOverrides.width && { width: { ideal: videoOverrides.width } }),
          ...(videoOverrides.height && { height: { ideal: videoOverrides.height } }),
          ...(videoOverrides.framerate && { frameRate: { ideal: videoOverrides.framerate } }),
        };
        this.log("Using encoder overrides for screen capture constraints:", captureOptions.video);
      }

      const stream = await this.screenCapture.start(captureOptions);

      if (stream) {
        // Get actual label from video track (e.g., "Screen 1", "window:App Name")
        const videoTrack = stream.getVideoTracks()[0];
        const label = videoTrack?.label || `Screen ${this.screenCapture.getCaptureCount()}`;

        const source = this.addMediaSource("screen", stream, label);

        this.setState("capturing", {
          hasVideo: true,
          isScreenShare: true,
        });

        return source;
      } else {
        // User cancelled
        if (this.sources.size > 0) {
          this.setState("capturing");
        } else {
          this.setState("idle");
        }
        return null;
      }
    } catch (error) {
      this.setState("error", {
        error: error instanceof Error ? error.message : String(error),
      });
      throw error;
    }
  }

  /**
   * Add a custom media source
   */
  addCustomSource(stream: MediaStream, label: string): MediaSource {
    return this.addMediaSource("custom", stream, label);
  }

  /**
   * Set source volume (for audio mixing)
   */
  setSourceVolume(sourceId: string, volume: number): void {
    const source = this.sources.get(sourceId);
    if (!source) return;

    // Allow boost up to 200% (+6dB)
    source.volume = Math.max(0, Math.min(2, volume));
    this.sources.set(sourceId, source);

    if (this.config.audioMixing) {
      this.audioMixer.setVolume(sourceId, source.volume);
    }

    this.emit("sourceUpdated", { source, changes: { volume: source.volume } });
  }

  /**
   * Mute/unmute a source
   */
  setSourceMuted(sourceId: string, muted: boolean): void {
    const source = this.sources.get(sourceId);
    if (!source) return;

    source.muted = muted;
    this.sources.set(sourceId, source);

    if (this.config.audioMixing) {
      if (muted) {
        this.audioMixer.mute(sourceId);
      } else {
        this.audioMixer.unmute(sourceId);
      }
    } else {
      // Mute the track directly
      source.stream.getAudioTracks().forEach((track) => {
        track.enabled = !muted;
      });
    }

    this.emit("sourceUpdated", { source, changes: { muted } });
    this.updateOutputStreamFromSources();
  }

  /**
   * Set source active state
   */
  setSourceActive(sourceId: string, active: boolean): void {
    const source = this.sources.get(sourceId);
    if (!source) return;

    source.active = active;
    this.sources.set(sourceId, source);

    this.emit("sourceUpdated", { source, changes: { active } });
    this.updateOutputStreamFromSources();
  }

  /**
   * Set master output volume (0-2 for up to +6dB boost)
   */
  setMasterVolume(volume: number): void {
    if (!this.config.audioMixing) return;
    this.audioMixer.setMasterVolume(volume);
  }

  /**
   * Get current master output volume
   */
  getMasterVolume(): number {
    if (!this.config.audioMixing) return 1;
    return this.audioMixer.getMasterVolume();
  }

  /**
   * Stop all capture
   */
  async stopCapture(): Promise<void> {
    this.log("Stopping all capture");

    // Remove all sources
    for (const id of Array.from(this.sources.keys())) {
      this.removeSource(id);
    }

    this.deviceManager.stopAllTracks();
    this.screenCapture.stop();
    this.outputStream = null;

    if (this.state !== "streaming") {
      this.setState("idle", {
        hasVideo: false,
        hasAudio: false,
        isScreenShare: false,
      });
    }
  }

  /**
   * Change quality profile
   */
  async setQualityProfile(profile: QualityProfile): Promise<void> {
    if (profile === this.currentProfile) return;

    const previousProfile = this.currentProfile;
    this.currentProfile = profile;

    this.log(`Changing quality profile: ${previousProfile} -> ${profile}`);

    // Update existing camera sources with new constraints
    for (const [_id, source] of this.sources) {
      if (source.type === "camera") {
        const videoTrack = source.stream.getVideoTracks()[0];
        if (videoTrack) {
          try {
            const constraints = getVideoConstraints(profile);
            await videoTrack.applyConstraints(constraints);
          } catch (error) {
            this.log("Failed to apply new constraints", error);
          }
        }
      }
    }

    this.emit("qualityChanged", { profile, previousProfile });
    this.setState(this.state, { activeProfile: profile });
  }

  /**
   * Set up permanent event handlers on the WhipClient.
   * Called after initial connection and after reconnection.
   */
  private setupWhipClientHandlers(): void {
    if (!this.whipClient) return;

    this.cleanupWhipClientHandlers();

    this.whipClientUnsubs.push(
      this.whipClient.on("stateChange", async (event) => {
        this.log("WHIP state changed", event);
        this.stateContext = {
          ...this.stateContext,
          connectionState: event.state,
        };

        if (event.state === "connected") {
          this.setState("streaming");
          this.startStatsPolling();
          this.reconnectionManager.reset();

          // Attach WebCodecs encoder transform if supported and codecs are aligned
          // This must happen AFTER connection is established (senders exist)
          if (this.useWebCodecs && this.encoderManager && this.whipClient) {
            this.log("Attempting to attach WebCodecs encoder transform");
            // Check if codec alignment allows encoded frame insertion
            const canUseEncoded = this.whipClient.canUseEncodedInsertion();
            this.log("canUseEncodedInsertion result:", canUseEncoded);
            if (canUseEncoded) {
              try {
                // Reconfigure encoder to match negotiated codec if in auto mode
                await this.reconfigureEncoderForNegotiatedCodec();

                this.whipClient.attachEncoderTransform(
                  this.encoderManager,
                  this.config.workers?.rtcTransform
                );
                this.encoderManager.start();
                this.log("WebCodecs encoder transform attached", {
                  videoCodec: this.whipClient.getNegotiatedVideoCodec(),
                  codecFamily: this.videoCodecFamily,
                  audioCodec: this.whipClient.getNegotiatedAudioCodec(),
                });
                this.emit("webCodecsActive", { active: true });

                // Start adaptive bitrate if enabled
                this.startBitrateAdaptation();

                // Enable direct frame output if compositor is active
                this.enableDirectFrameOutput();
              } catch (err) {
                this.log(
                  "Failed to attach encoder transform, continuing with browser encoding",
                  err
                );
                if (this.encoderManager) {
                  this.encoderManager.destroy();
                  this.encoderManager = null;
                }
              }
            } else {
              this.log("Codec alignment check failed, using browser encoding", {
                videoCodec: this.whipClient.getNegotiatedVideoCodec(),
                audioCodec: this.whipClient.getNegotiatedAudioCodec(),
              });
              if (this.encoderManager) {
                this.encoderManager.destroy();
                this.encoderManager = null;
              }
            }
          }
        } else if (event.state === "failed" || event.state === "disconnected") {
          // Skip reconnection/error handling if user intentionally stopped streaming
          if (this.isStoppingIntentionally) {
            return;
          }
          if (this.state === "streaming" && this.config.reconnection?.enabled !== false) {
            this.handleConnectionLost();
          } else {
            this.setState("error", {
              error: event.state === "failed" ? "Connection failed" : "Connection lost",
            });
            this.stopStatsPolling();
          }
        }
      })
    );

    this.whipClientUnsubs.push(
      this.whipClient.on("error", (event) => {
        if (this.isStoppingIntentionally) {
          return;
        }
        this.emit("error", { error: event.message, recoverable: false });
      })
    );
  }

  private cleanupWhipClientHandlers(): void {
    this.whipClientUnsubs.forEach((fn) => fn());
    this.whipClientUnsubs = [];
  }

  /**
   * Resolve the initial codec family to use for encoding.
   * 'auto' defaults to h264 (will be reconfigured after SDP negotiation).
   */
  private resolveInitialCodecFamily(): VideoCodecFamily {
    const pref = this.config.videoCodec ?? "auto";
    if (pref === "auto") return "h264";
    return pref;
  }

  /**
   * After SDP negotiation, reconfigure encoder to match the server-negotiated codec.
   * Only reconfigures if videoCodec is 'auto' and the negotiated codec differs from initial.
   */
  private async reconfigureEncoderForNegotiatedCodec(): Promise<void> {
    if (!this.whipClient || !this.encoderManager) return;

    const negotiatedFamily = this.whipClient.getNegotiatedVideoCodecFamily();
    if (!negotiatedFamily) return;

    // Skip if user explicitly chose a codec (not 'auto')
    const pref = this.config.videoCodec ?? "auto";
    if (pref !== "auto") {
      this.log("Codec explicitly set, skipping negotiation-based reconfigure", { pref });
      return;
    }

    // Skip if already configured for this codec
    if (negotiatedFamily === this.videoCodecFamily) {
      this.log("Encoder already configured for negotiated codec", { negotiatedFamily });
      return;
    }

    this.log("Reconfiguring encoder for negotiated codec", {
      from: this.videoCodecFamily,
      to: negotiatedFamily,
    });

    const encoderProfile = this.currentProfile === "auto" ? "broadcast" : this.currentProfile;
    const newConfig = createMultiCodecEncoderConfig(
      encoderProfile,
      negotiatedFamily,
      this.encoderOverrides
    );

    await this.encoderManager.updateConfig(newConfig);
    this.videoCodecFamily = negotiatedFamily;

    this.log("Encoder reconfigured for negotiated codec", {
      codec: newConfig.video.codec,
      bitrate: newConfig.video.bitrate,
      keyframeInterval: newConfig.keyframeInterval,
    });
  }

  /**
   * Start streaming to WHIP endpoint
   */
  async startStreaming(): Promise<void> {
    if (!this.outputStream) {
      throw new Error("No media source available. Add a camera or screen share first.");
    }

    this.log("Starting streaming");
    // New session should start from primary WHIP endpoint.
    this.currentEndpointIndex = 0;
    this.setState("connecting");

    try {
      // Create WHIP client
      this.whipClient = new WhipClient({
        whipUrl: this.getCurrentWhipUrl(),
        iceServers: this.config.iceServers,
        debug: this.config.debug,
      });

      // Set up WHIP event handlers
      this.setupWhipClientHandlers();

      // Resume audio context if needed
      if (this.config.audioMixing) {
        await this.audioMixer.resume();
      }

      // Initialize WebCodecs encoder if enabled and RTCRtpScriptTransform is supported
      if (this.useWebCodecs && isRTCRtpScriptTransformSupported()) {
        this.log("Initializing WebCodecs encoder (Path C: RTCRtpScriptTransform)");
        try {
          this.encoderManager = new EncoderManager({
            debug: this.config.debug,
            workerUrl: this.config.workers?.encoder,
          });

          // Set up encoder event forwarding
          this.encoderManager.on("error", (event) => {
            this.emit("error", { error: event.message, recoverable: !event.fatal });

            // On fatal encoder error during streaming, reconnect without WebCodecs
            if (event.fatal && this.state === "streaming") {
              this.log("Fatal encoder error, reconnecting without WebCodecs");
              this.handleEncoderFailure();
            }
          });

          this.encoderManager.on("stats", (stats) => {
            this.log("Encoder stats", stats);
          });

          // Initialize encoder with output stream
          // Map quality profile to encoder profile (handle 'auto' by defaulting to 'broadcast')
          const encoderProfile = this.currentProfile === "auto" ? "broadcast" : this.currentProfile;
          // Resolve codec family: explicit config, or default to h264 (will be reconfigured after negotiation)
          const initialCodec = this.resolveInitialCodecFamily();
          this.videoCodecFamily = initialCodec;
          const encoderConfig = createMultiCodecEncoderConfig(
            encoderProfile,
            initialCodec,
            this.encoderOverrides
          );
          this.log("Encoder config with overrides:", encoderConfig);
          await this.encoderManager.initialize(this.outputStream, encoderConfig);
          this.log("WebCodecs encoder initialized");
        } catch (err) {
          // If encoder initialization fails, continue without it
          this.log(
            "WebCodecs encoder initialization failed, falling back to browser encoding",
            err
          );
          if (this.encoderManager) {
            this.encoderManager.destroy();
            this.encoderManager = null;
          }
        }
      } else if (this.useWebCodecs) {
        this.log(
          "WebCodecs requested but RTCRtpScriptTransform not supported, using browser encoding"
        );
      }

      // Connect via standard MediaStream path
      // The encoder transform will be attached after connection is established
      await this.whipClient.connect(this.outputStream);
    } catch (error) {
      // Clean up encoder if connection fails
      if (this.encoderManager) {
        this.encoderManager.destroy();
        this.encoderManager = null;
      }
      this.setState("error", {
        error: error instanceof Error ? error.message : String(error),
      });
      throw error;
    }
  }

  /**
   * Handle encoder failure - reconnect without WebCodecs
   */
  private async handleEncoderFailure(): Promise<void> {
    this.log("Handling encoder failure - reconnecting without WebCodecs");
    this.setState("reconnecting");
    this.stopStatsPolling();
    this.disableDirectFrameOutput();
    this.stopBitrateAdaptation();

    // Clean up encoder
    if (this.encoderManager) {
      this.encoderManager.destroy();
      this.encoderManager = null;
    }

    // Disable WebCodecs for this session
    this.useWebCodecs = false;

    // Clean up old WHIP client
    if (this.whipClient) {
      try {
        await this.whipClient.disconnect();
      } finally {
        this.whipClient.destroy();
        this.whipClient = null;
      }
    }

    // Reconnect without WebCodecs
    if (!this.outputStream) {
      this.setState("error", { error: "No output stream available for reconnection" });
      return;
    }

    try {
      this.whipClient = new WhipClient({
        whipUrl: this.getNextWhipUrl(),
        iceServers: this.config.iceServers,
        debug: this.config.debug,
      });

      // Set up event handlers (will use browser encoding since useWebCodecs is now false)
      this.setupWhipClientHandlers();

      await this.whipClient.connect(this.outputStream);
    } catch (error) {
      this.setState("error", {
        error: `Reconnection failed: ${error instanceof Error ? error.message : String(error)}`,
      });
    }
  }

  /**
   * Handle connection lost - trigger reconnection
   */
  private handleConnectionLost(): void {
    this.log("Connection lost, starting reconnection");
    this.setState("reconnecting");
    this.stopStatsPolling();
    this.stopBitrateAdaptation();

    this.reconnectionManager.start(async () => {
      // Clean up old client handlers and connection
      this.cleanupWhipClientHandlers();
      if (this.whipClient) {
        try {
          await this.whipClient.disconnect();
        } finally {
          this.whipClient.destroy();
          this.whipClient = null;
        }
      }

      // Create new client and reconnect
      if (!this.outputStream) {
        throw new Error("No output stream available");
      }

      this.whipClient = new WhipClient({
        whipUrl: this.getNextWhipUrl(),
        iceServers: this.config.iceServers,
        debug: this.config.debug,
      });

      // Set up permanent event handlers (includes WebCodecs re-attachment)
      this.setupWhipClientHandlers();

      // Wait for connection to complete
      await new Promise<void>((resolve, reject) => {
        const timeout = setTimeout(() => {
          reject(new Error("Connection timeout"));
        }, 30000);

        // One-time listener just to signal reconnection success/failure
        const onStateChange = (event: { state: string }) => {
          if (event.state === "connected") {
            clearTimeout(timeout);
            this.whipClient?.off("stateChange", onStateChange);
            resolve();
          } else if (event.state === "failed") {
            clearTimeout(timeout);
            this.whipClient?.off("stateChange", onStateChange);
            reject(new Error("Connection failed"));
          }
        };

        this.whipClient!.on("stateChange", onStateChange);
        this.whipClient!.connect(this.outputStream!).catch(reject);
      });
    });
  }

  /**
   * Stop streaming
   */
  async stopStreaming(): Promise<void> {
    this.log("Stopping streaming");
    this.isStoppingIntentionally = true;

    try {
      this.stopStatsPolling();
      this.disableDirectFrameOutput();
      this.stopBitrateAdaptation();
      this.reconnectionManager.stop();

      // Stop encoder
      if (this.encoderManager) {
        await this.encoderManager.stop();
        this.encoderManager.destroy();
        this.encoderManager = null;
      }

      if (this.whipClient) {
        await this.whipClient.disconnect();
        this.whipClient.destroy();
        this.whipClient = null;
      }

      if (this.sources.size > 0) {
        this.setState("capturing");
      } else {
        this.setState("idle");
      }

      this.stateContext = {
        ...this.stateContext,
        connectionState: "disconnected",
      };
    } finally {
      this.isStoppingIntentionally = false;
    }
  }

  /**
   * Switch video device
   */
  async switchVideoDevice(deviceId: string): Promise<void> {
    const newTrack = await this.deviceManager.replaceVideoTrack(deviceId, this.currentProfile);

    if (newTrack && this.whipClient) {
      const pc = this.whipClient.getPeerConnection();
      if (pc) {
        const sender = pc.getSenders().find((s) => s.track?.kind === "video");
        if (sender) {
          await sender.replaceTrack(newTrack);
        }
      }
    }
  }

  /**
   * Switch audio device
   */
  async switchAudioDevice(deviceId: string): Promise<void> {
    const newTrack = await this.deviceManager.replaceAudioTrack(deviceId, this.currentProfile);

    if (newTrack && this.whipClient) {
      const pc = this.whipClient.getPeerConnection();
      if (pc) {
        const sender = pc.getSenders().find((s) => s.track?.kind === "audio");
        if (sender) {
          await sender.replaceTrack(newTrack);
        }
      }
    }
  }

  /**
   * Start stats polling
   */
  private startStatsPolling(): void {
    if (this.statsInterval) return;

    this.statsInterval = setInterval(async () => {
      // Guard against overlapping async calls
      if (this.statsInFlight) return;
      this.statsInFlight = true;

      try {
        const stats = await this.getStats();
        if (stats) {
          this.lastStats = stats;
          this.emit("statsUpdate", stats);
        }
      } finally {
        this.statsInFlight = false;
      }
    }, 1000);
  }

  /**
   * Stop stats polling
   */
  private stopStatsPolling(): void {
    if (this.statsInterval) {
      clearInterval(this.statsInterval);
      this.statsInterval = null;
    }
  }

  /**
   * Get current stats
   */
  async getStats(): Promise<IngestStats | null> {
    if (!this.whipClient) return null;

    const report = await this.whipClient.getStats();
    if (!report) return null;

    const stats: IngestStats = {
      video: {
        bytesSent: 0,
        packetsSent: 0,
        packetsLost: 0,
        framesEncoded: 0,
        framesPerSecond: 0,
        bitrate: 0,
      },
      audio: {
        bytesSent: 0,
        packetsSent: 0,
        packetsLost: 0,
        bitrate: 0,
      },
      connection: {
        rtt: 0,
        state: this.whipClient.getPeerConnection()?.connectionState ?? "new",
        iceState: this.whipClient.getPeerConnection()?.iceConnectionState ?? "new",
      },
      timestamp: Date.now(),
    };

    // Calculate bitrate from previous stats
    const prevStats = this.lastStats;

    report.forEach((stat) => {
      if (stat.type === "outbound-rtp") {
        const rtpStat = stat as RTCOutboundRtpStreamStats;
        if (rtpStat.kind === "video") {
          stats.video.bytesSent = rtpStat.bytesSent ?? 0;
          stats.video.packetsSent = rtpStat.packetsSent ?? 0;
          stats.video.framesEncoded = rtpStat.framesEncoded ?? 0;
          stats.video.framesPerSecond = rtpStat.framesPerSecond ?? 0;

          // Calculate bitrate
          if (prevStats) {
            const timeDiff = (stats.timestamp - prevStats.timestamp) / 1000;
            const bytesDiff = stats.video.bytesSent - prevStats.video.bytesSent;
            stats.video.bitrate = Math.round((bytesDiff * 8) / timeDiff);
          }
        } else if (rtpStat.kind === "audio") {
          stats.audio.bytesSent = rtpStat.bytesSent ?? 0;
          stats.audio.packetsSent = rtpStat.packetsSent ?? 0;

          if (prevStats) {
            const timeDiff = (stats.timestamp - prevStats.timestamp) / 1000;
            const bytesDiff = stats.audio.bytesSent - prevStats.audio.bytesSent;
            stats.audio.bitrate = Math.round((bytesDiff * 8) / timeDiff);
          }
        }
      } else if (
        stat.type === "candidate-pair" &&
        (stat as RTCIceCandidatePairStats).state === "succeeded"
      ) {
        stats.connection.rtt = (stat as RTCIceCandidatePairStats).currentRoundTripTime ?? 0;
      }
    });

    return stats;
  }

  // ============================================================================
  // Phase 3: Compositor
  // ============================================================================

  /**
   * Enable the compositor for multi-source composition
   * Call this before adding sources if you want compositor-based output
   */
  async enableCompositor(config?: Partial<CompositorConfig>): Promise<void> {
    this.log("enableCompositor called", { alreadyEnabled: !!this.sceneManager });

    if (this.sceneManager) {
      this.log("Compositor already enabled");
      return;
    }

    const compositorConfig = { ...DEFAULT_COMPOSITOR_CONFIG, ...this.config.compositor, ...config };
    this.log("Creating SceneManager with config", compositorConfig);
    this.sceneManager = new SceneManager(compositorConfig, {
      workerUrl: this.config.workers?.compositor,
    });
    this.compositorBaseConfig = compositorConfig;

    // Initialize the compositor
    try {
      this.log("Initializing SceneManager...");
      await this.sceneManager.initialize();
      this.log("SceneManager initialized successfully");
    } catch (e) {
      // If initialization fails, clean up and re-throw
      this.sceneManager = null;
      const message = e instanceof Error ? e.message : String(e);
      this.log("Compositor initialization failed:", message);
      throw new Error(`Compositor initialization failed: ${message}`);
    }

    // Verify sceneManager is still set after async initialize
    if (!this.sceneManager) {
      this.log("ERROR: SceneManager was unexpectedly null after initialization");
      throw new Error("SceneManager was unexpectedly null after initialization");
    }

    this.log("SceneManager is valid, getting active scene...");

    // Bind existing sources to the compositor and add as layers
    const activeScene = this.sceneManager.getActiveScene();
    this.log("Compositor active scene:", activeScene?.id ?? "none");

    for (const [id, source] of this.sources) {
      if (!this.sceneManager) break; // Guard against concurrent disable
      // Bind source for frame extraction
      this.sceneManager.bindSource(id, source.stream);
      // Add layer to active scene
      if (activeScene) {
        this.sceneManager.addLayer(activeScene.id, id);
      }
    }

    // Forward compositor events
    if (this.sceneManager) {
      this.sceneManager.on("error", (event) => {
        this.emit("error", { error: event.message, recoverable: true });
      });
    }

    this.log("Compositor enabled", compositorConfig);

    // If encoder is already active, enable direct frame output
    if (this.encoderManager && this.isWebCodecsActive()) {
      this.enableDirectFrameOutput();
    }

    // Update output to use compositor
    this.updateOutputStreamFromSources();
  }

  /**
   * Disable the compositor
   */
  disableCompositor(): void {
    if (this.sceneManager) {
      this.disableDirectFrameOutput();
      this.sceneManager.destroy();
      this.sceneManager = null;
      this.log("Compositor disabled");
      this.updateOutputStreamFromSources();
    }
  }

  /**
   * Get the scene manager for compositor control
   */
  getSceneManager(): SceneManager | null {
    return this.sceneManager;
  }

  /**
   * Check if compositor is enabled
   */
  isCompositorEnabled(): boolean {
    return this.sceneManager !== null && this.sceneManager.isInitialized();
  }

  // ============================================================================
  // Getters
  // ============================================================================

  getState(): IngestState {
    return this.state;
  }

  getStateContext(): IngestStateContextV2 {
    return { ...this.stateContext };
  }

  getMediaStream(): MediaStream | null {
    return this.outputStream;
  }

  getSources(): MediaSource[] {
    return Array.from(this.sources.values());
  }

  getSource(id: string): MediaSource | undefined {
    return this.sources.get(id);
  }

  getQualityProfile(): QualityProfile {
    return this.currentProfile;
  }

  getDeviceManager(): DeviceManager {
    return this.deviceManager;
  }

  getScreenCapture(): ScreenCapture {
    return this.screenCapture;
  }

  getAudioMixer(): AudioMixer {
    return this.audioMixer;
  }

  getReconnectionManager(): ReconnectionManager {
    return this.reconnectionManager;
  }

  async getDevices(): Promise<DeviceInfo[]> {
    return this.deviceManager.enumerateDevices();
  }

  isStreaming(): boolean {
    return this.state === "streaming";
  }

  isCapturing(): boolean {
    return this.state === "capturing" || this.state === "streaming";
  }

  isReconnecting(): boolean {
    return this.state === "reconnecting";
  }

  /**
   * Set whether to use WebCodecs encoding
   * Only takes effect before streaming starts (cannot change mid-stream)
   */
  setUseWebCodecs(enabled: boolean): void {
    if (this.state === "streaming") {
      this.log("Cannot change useWebCodecs while streaming");
      return;
    }
    this.useWebCodecs = enabled;
    this.log("useWebCodecs set to", enabled);
  }

  /**
   * Set encoder overrides (resolution, bitrate, framerate, etc.)
   * Only takes effect before streaming starts (cannot change mid-stream)
   */
  setEncoderOverrides(overrides: EncoderOverrides): void {
    if (this.state === "streaming") {
      this.log("Cannot change encoder overrides while streaming");
      return;
    }
    this.encoderOverrides = overrides;
    this.log("Encoder overrides set:", overrides);

    if (this.sceneManager) {
      const baseConfig = this.compositorBaseConfig ?? this.sceneManager.getConfig();
      const targetWidth = overrides.video?.width ?? baseConfig.width;
      const targetHeight = overrides.video?.height ?? baseConfig.height;
      const targetFrameRate = overrides.video?.framerate ?? baseConfig.frameRate;
      const updated = this.sceneManager.updateOutputConfig({
        width: targetWidth,
        height: targetHeight,
        frameRate: targetFrameRate,
      });
      if (updated) {
        this.updateOutputStreamFromSources();
      }
    }
  }

  /**
   * Get current encoder overrides
   */
  getEncoderOverrides(): EncoderOverrides {
    return this.encoderOverrides;
  }

  /**
   * Get current useWebCodecs setting
   */
  getUseWebCodecs(): boolean {
    return this.useWebCodecs;
  }

  /**
   * Get the encoder manager (for advanced use cases)
   */
  getEncoderManager(): EncoderManager | null {
    return this.encoderManager;
  }

  /**
   * Check if WebCodecs encoding is active
   */
  isWebCodecsActive(): boolean {
    return this.encoderManager !== null && this.whipClient?.hasEncoderTransform() === true;
  }

  isAdaptiveBitrateActive(): boolean {
    return this.bitrateAdaptation !== null;
  }

  getCurrentBitrate(): number | null {
    return this.bitrateAdaptation?.bitrate ?? null;
  }

  getCongestionLevel(): string | null {
    return this.bitrateAdaptation?.congestionLevel ?? null;
  }

  // ============================================================================
  // Direct Frame Output (compositor → encoder bypass)
  // ============================================================================

  /**
   * When both compositor and WebCodecs encoder are active, feed composited
   * VideoFrames directly to the encoder — skipping captureStream() entirely.
   */
  private enableDirectFrameOutput(): void {
    if (!this.sceneManager || !this.encoderManager) return;

    this.sceneManager.setFrameCallback((frame: VideoFrame) => {
      if (this.encoderManager) {
        this.encoderManager.feedVideoFrame(frame);
      } else {
        frame.close();
      }
    });
    this.log("Direct compositor → encoder frame pipeline enabled");
  }

  private disableDirectFrameOutput(): void {
    if (this.sceneManager) {
      this.sceneManager.setFrameCallback(null);
    }
  }

  // ============================================================================
  // Adaptive Bitrate
  // ============================================================================

  private startBitrateAdaptation(): void {
    if (this.config.adaptiveBitrate === false) return;
    if (!this.whipClient || !this.encoderManager) return;

    const pc = this.whipClient.getPeerConnection();
    if (!pc) return;

    this.stopBitrateAdaptation();

    const encoderConfig = this.encoderManager.getConfig();
    this.bitrateAdaptation = new BitrateAdaptation({
      pc,
      encoder: this.encoderManager,
      maxBitrate: encoderConfig?.video.bitrate ?? 4_500_000,
    });

    this.bitrateAdaptation.on("bitrateChanged", (event) => {
      this.log("Bitrate adapted", event);
      this.emit("bitrateChanged", event);
    });

    this.bitrateAdaptation.on("congestionChanged", (event) => {
      this.log("Congestion changed", event);
      this.emit("congestionChanged", event);
    });

    this.bitrateAdaptation.start();
    this.log("Adaptive bitrate started", { maxBitrate: encoderConfig?.video.bitrate });
  }

  private stopBitrateAdaptation(): void {
    if (this.bitrateAdaptation) {
      this.bitrateAdaptation.destroy();
      this.bitrateAdaptation = null;
    }
  }

  // ============================================================================
  // Recording
  // ============================================================================

  /**
   * Start recording encoded chunks to a WebM container.
   * Requires WebCodecs encoding to be active (encoder must be running).
   * Recording works independently of streaming — can record while not streaming
   * as long as the encoder is running.
   */
  startRecording(): void {
    if (this.recordingManager?.isRecording) {
      this.log("Recording already in progress");
      return;
    }

    if (!this.encoderManager) {
      this.log("Cannot record: no encoder active (WebCodecs required)");
      this.emit("error", { error: "Recording requires WebCodecs encoding", recoverable: true });
      return;
    }

    // H.264 is not valid in WebM — recording requires VP9 or AV1
    if (this.videoCodecFamily === "h264") {
      this.log("Cannot record: H.264 is not supported in WebM containers. Use VP9 or AV1.");
      this.emit("error", {
        error: "Recording requires VP9 or AV1 codec (H.264 not supported in WebM)",
        recoverable: true,
      });
      return;
    }

    const videoCodec = this.videoCodecFamily === "av1" ? ("V_AV1" as const) : ("V_VP9" as const);
    const encoderConfig = this.encoderManager.getConfig();

    const muxerOptions: WebMWriterOptions = {
      video: {
        width: encoderConfig?.video.width ?? 1920,
        height: encoderConfig?.video.height ?? 1080,
      },
      audio: {
        sampleRate: encoderConfig?.audio.sampleRate ?? 48000,
        channels: encoderConfig?.audio.numberOfChannels ?? 2,
      },
      videoCodec,
    };

    this.recordingManager = new RecordingManager({ muxerOptions });

    this.recordingManager.on("progress", (event) => {
      this.log("Recording progress", event);
      this.emit("recordingProgress", { duration: event.duration, fileSize: event.fileSize });
    });

    this.recordingManager.on("stopped", (event) => {
      this.log("Recording stopped", {
        duration: event.duration,
        fileSize: event.fileSize,
      });
    });

    this.recordingManager.start(this.encoderManager);
    this.log("Recording started", { videoCodec, muxerOptions });
    this.emit("recordingStarted", undefined as any);
  }

  /**
   * Stop recording and return the WebM blob.
   * Returns null if no recording is in progress.
   */
  stopRecording(): Blob | null {
    if (!this.recordingManager) return null;

    const duration = this.recordingManager.duration;
    const fileSize = this.recordingManager.fileSize;
    const blob = this.recordingManager.stop();
    this.recordingManager.destroy();
    this.recordingManager = null;
    if (blob) {
      this.emit("recordingStopped", { blob, duration, fileSize });
    }
    return blob;
  }

  /**
   * Pause recording (chunks are discarded while paused).
   */
  pauseRecording(): void {
    if (!this.recordingManager) return;
    this.recordingManager.pause();
    this.emit("recordingPaused", undefined as any);
  }

  /**
   * Resume recording after pause.
   */
  resumeRecording(): void {
    if (!this.recordingManager) return;
    this.recordingManager.resume();
    this.emit("recordingResumed", undefined as any);
  }

  /**
   * Check if recording is in progress.
   */
  isRecordingActive(): boolean {
    return this.recordingManager?.isRecording === true;
  }

  /**
   * Get current recording duration in milliseconds.
   */
  getRecordingDuration(): number {
    return this.recordingManager?.duration ?? 0;
  }

  /**
   * Get current recording file size in bytes.
   */
  getRecordingFileSize(): number {
    return this.recordingManager?.fileSize ?? 0;
  }

  /**
   * Add a MediaFileSource as a streaming source.
   * Wraps addCustomSource with type "media" and auto-handles the "ended" event.
   */
  addMediaFileSource(
    source: { getStream(): MediaStream | null; on(event: string, handler: Function): () => void },
    label = "Media"
  ): MediaSource | null {
    const stream = source.getStream();
    if (!stream) return null;

    const ms = this.addCustomSource(stream, label);

    // Auto-deactivate when playback ends
    const unsub = source.on("ended", () => {
      this.removeSource(ms.id);
      unsub();
    });

    return ms;
  }

  /**
   * Get the current video codec family in use.
   */
  getVideoCodecFamily(): VideoCodecFamily {
    return this.videoCodecFamily;
  }

  /**
   * Destroy the controller
   */
  destroy(): void {
    this.log("Destroying IngestControllerV2");
    this.stopStatsPolling();
    this.disableDirectFrameOutput();
    this.stopBitrateAdaptation();
    this.cleanupWhipClientHandlers();
    this.eventForwardingUnsubs.forEach((fn) => fn());
    this.eventForwardingUnsubs = [];
    this.reconnectionManager.destroy();

    // Stop recording
    if (this.recordingManager) {
      this.recordingManager.destroy();
      this.recordingManager = null;
    }

    // Destroy encoder
    if (this.encoderManager) {
      this.encoderManager.destroy();
      this.encoderManager = null;
    }

    if (this.whipClient) {
      this.whipClient.destroy();
      this.whipClient = null;
    }

    // Destroy compositor
    if (this.sceneManager) {
      this.sceneManager.destroy();
      this.sceneManager = null;
    }

    // Remove all sources
    for (const id of Array.from(this.sources.keys())) {
      this.removeSource(id);
    }

    this.deviceManager.destroy();
    this.screenCapture.destroy();
    this.audioMixer.destroy();
    this.removeAllListeners();

    this.setState("destroyed");
  }
}
