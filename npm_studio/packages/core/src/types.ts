/**
 * StreamCrafter Core Types
 * Type definitions for browser-based streaming with WebCodecs
 */

// ============================================================================
// State Types
// ============================================================================

export type IngestState =
  | "idle"
  | "requesting_permissions"
  | "capturing"
  | "connecting"
  | "streaming"
  | "reconnecting"
  | "error"
  | "destroyed";

export type WhipConnectionState = "disconnected" | "connecting" | "connected" | "failed" | "closed";

export type QualityProfile = "professional" | "broadcast" | "conference" | "auto";

// ============================================================================
// Device Types
// ============================================================================

export interface DeviceInfo {
  deviceId: string;
  kind: "audioinput" | "videoinput" | "audiooutput";
  label: string;
  groupId: string;
}

export interface CaptureOptions {
  videoDeviceId?: string;
  audioDeviceId?: string;
  facingMode?: "user" | "environment";
  profile?: QualityProfile;
  customConstraints?: MediaStreamConstraints;
}

export interface ScreenCaptureOptions {
  video?: boolean | DisplayMediaStreamOptions["video"];
  audio?: boolean | DisplayMediaStreamOptions["audio"];
  preferCurrentTab?: boolean;
  systemAudio?: "include" | "exclude";
  /** Cursor visibility in screen capture */
  cursor?: "always" | "motion" | "never";
  /** Allow switching surfaces during capture (Chrome 107+) */
  surfaceSwitching?: boolean;
  /** Include/exclude self browser surface (Chrome 107+) */
  selfBrowserSurface?: "include" | "exclude";
  /** Monitor type surfaces (Chrome 119+) */
  monitorTypeSurfaces?: "include" | "exclude";
}

// ============================================================================
// Quality Profile Types
// ============================================================================

export interface QualityProfileInfo {
  id: QualityProfile;
  name: string;
  description: string;
}

export interface AudioConstraintProfile {
  echoCancellation: boolean;
  noiseSuppression: boolean;
  autoGainControl: boolean;
  sampleRate: number;
  channelCount: number;
  latency?: number;
}

export interface VideoConstraintProfile {
  width: { ideal: number };
  height: { ideal: number };
  frameRate: { ideal: number };
}

// ============================================================================
// WHIP Client Types
// ============================================================================

export interface WhipClientConfig {
  whipUrl: string;
  iceServers?: RTCIceServer[];
  debug?: boolean;
}

export interface WhipClientEvents {
  stateChange: { state: WhipConnectionState; previousState: WhipConnectionState };
  stats: { video: RTCOutboundStats; audio: RTCOutboundStats };
  error: { message: string; error?: Error };
  iceCandidate: { candidate: RTCIceCandidate | null };
}

export interface RTCOutboundStats {
  bytesSent: number;
  packetsSent: number;
  packetsLost: number;
  bitrate?: number;
}

// ============================================================================
// Ingest Controller Types
// ============================================================================

export interface IngestControllerConfig {
  whipUrl: string;
  iceServers?: RTCIceServer[];
  profile?: QualityProfile;
  useWebCodecs?: boolean;
  debug?: boolean;
}

export interface IngestStateContext {
  reason?: string;
  error?: string;
  connectionState?: WhipConnectionState;
  hasVideo?: boolean;
  hasAudio?: boolean;
  isScreenShare?: boolean;
}

export interface IngestControllerEvents {
  stateChange: { state: IngestState; context?: IngestStateContext };
  statsUpdate: IngestStats;
  deviceChange: { devices: DeviceInfo[] };
  error: { error: string; recoverable: boolean };
}

export interface IngestStats {
  video: {
    bytesSent: number;
    packetsSent: number;
    packetsLost: number;
    framesEncoded: number;
    framesPerSecond: number;
    bitrate: number;
  };
  audio: {
    bytesSent: number;
    packetsSent: number;
    packetsLost: number;
    bitrate: number;
  };
  connection: {
    rtt: number;
    state: RTCPeerConnectionState;
    iceState: RTCIceConnectionState;
  };
  timestamp: number;
}

// ============================================================================
// Browser Capability Types
// ============================================================================

export interface BrowserCapabilities {
  webcodecs: {
    videoEncoder: boolean;
    audioEncoder: boolean;
    mediaStreamTrackProcessor: boolean;
    mediaStreamTrackGenerator: boolean;
  };
  webrtc: {
    peerConnection: boolean;
    replaceTrack: boolean;
    insertableStreams: boolean;
    scriptTransform: boolean;
  };
  mediaDevices: {
    getUserMedia: boolean;
    getDisplayMedia: boolean;
    enumerateDevices: boolean;
  };
  recommended: "webcodecs" | "mediastream";
}

// ============================================================================
// Encoder Types
// ============================================================================

export interface VideoEncoderSettings {
  codec: string;
  width: number;
  height: number;
  bitrate: number;
  framerate: number;
}

export interface AudioEncoderSettings {
  codec: string;
  sampleRate: number;
  numberOfChannels: number;
  bitrate: number;
}

export interface EncoderConfig {
  video: VideoEncoderSettings;
  audio: AudioEncoderSettings;
}

/**
 * Partial overrides for encoder settings
 * Allows UI to override individual settings from the profile defaults
 */
export interface EncoderOverrides {
  video?: {
    width?: number;
    height?: number;
    bitrate?: number;
    framerate?: number;
  };
  audio?: {
    bitrate?: number;
    sampleRate?: number;
    numberOfChannels?: number;
  };
}

// ============================================================================
// Worker Message Types
// ============================================================================

export type WorkerMessageType =
  | "initialize"
  | "videoFrame"
  | "audioData"
  | "start"
  | "stop"
  | "updateConfig"
  | "ready"
  | "error"
  | "stats";

export interface WorkerMessage {
  type: WorkerMessageType;
  data?: unknown;
}

export interface WorkerInitMessage extends WorkerMessage {
  type: "initialize";
  data: EncoderConfig;
}

export interface WorkerFrameMessage extends WorkerMessage {
  type: "videoFrame";
  data: {
    frame: VideoFrame;
    timestamp: number;
  };
}

export interface WorkerAudioMessage extends WorkerMessage {
  type: "audioData";
  data: {
    audioData: AudioData;
    timestamp: number;
  };
}

// ============================================================================
// Multi-Source Types (Phase 2)
// ============================================================================

export type SourceType = "camera" | "screen" | "custom";

export interface MediaSource {
  id: string;
  type: SourceType;
  stream: MediaStream;
  label: string;
  active: boolean;
  muted: boolean;
  volume: number; // 0.0 - 2.0 for audio mixing (200% boost)
  primaryVideo: boolean; // Is this the primary video source for streaming
}

export interface SourceAddedEvent {
  source: MediaSource;
}

export interface SourceRemovedEvent {
  sourceId: string;
}

export interface SourceUpdatedEvent {
  source: MediaSource;
  changes: Partial<MediaSource>;
}

// ============================================================================
// Audio Mixer Types (Phase 2)
// ============================================================================

export interface AudioMixerConfig {
  sampleRate?: number;
  channelCount?: number;
}

export interface AudioMixerEvents {
  outputChanged: { stream: MediaStream };
  sourceAdded: { sourceId: string };
  sourceRemoved: { sourceId: string };
  levelUpdate: { level: number; peakLevel: number }; // 0-1 normalized levels for VU meter
  error: { message: string; error?: Error };
}

export interface AudioSourceOptions {
  volume?: number;
  muted?: boolean;
  pan?: number; // -1.0 (left) to 1.0 (right)
}

// ============================================================================
// Reconnection Types (Phase 2)
// ============================================================================

export interface ReconnectionConfig {
  enabled: boolean;
  maxAttempts: number;
  baseDelay: number; // ms
  maxDelay: number; // ms
  backoffMultiplier: number;
}

export interface ReconnectionState {
  isReconnecting: boolean;
  attemptNumber: number;
  nextAttemptIn: number | null; // ms until next attempt
  lastError: string | null;
}

export interface ReconnectionEvents {
  attemptStart: { attempt: number; delay: number };
  attemptSuccess: undefined;
  attemptFailed: { attempt: number; error: string };
  exhausted: { totalAttempts: number };
}

// ============================================================================
// Extended Ingest Controller Types (Phase 2)
// ============================================================================

export interface IngestControllerConfigV2 extends IngestControllerConfig {
  whipUrls?: string[];
  reconnection?: Partial<ReconnectionConfig>;
  audioMixing?: boolean;
  compositor?: Partial<CompositorConfig>;
}

export interface IngestControllerEventsV2 extends IngestControllerEvents {
  sourceAdded: SourceAddedEvent;
  sourceRemoved: SourceRemovedEvent;
  sourceUpdated: SourceUpdatedEvent;
  qualityChanged: { profile: QualityProfile; previousProfile: QualityProfile };
  reconnectionAttempt: { attempt: number; maxAttempts: number };
  reconnectionSuccess: undefined;
  reconnectionFailed: { error: string };
  webCodecsActive: { active: boolean };
}

export interface IngestStateContextV2 extends IngestStateContext {
  sources?: MediaSource[];
  activeProfile?: QualityProfile;
  reconnection?: ReconnectionState;
}

// ============================================================================
// Scene & Layer Types (Phase 3 - Compositor)
// ============================================================================

export interface Scene {
  id: string;
  name: string;
  layers: Layer[];
  backgroundColor: string; // hex color for empty areas
}

export interface Layer {
  id: string;
  sourceId: string; // references MediaSource.id
  visible: boolean;
  locked: boolean;
  zIndex: number;
  transform: LayerTransform;
  scalingMode: ScalingMode; // How source is scaled to fit layer bounds
}

export interface LayerTransform {
  // Position (0-1 relative to canvas)
  x: number;
  y: number;
  // Size (0-1 relative to canvas)
  width: number;
  height: number;
  // Effects
  opacity: number; // 0-1
  rotation: number; // degrees
  borderRadius: number; // pixels
  // Crop (0-1 from each edge)
  crop: CropConfig;
}

export interface CropConfig {
  top: number;
  right: number;
  bottom: number;
  left: number;
}

// ============================================================================
// Layout Types (Phase 3 - Compositor)
// ============================================================================

export type LayoutMode =
  | "solo" // Single source fullscreen
  | "pip-br" // PiP bottom-right
  | "pip-bl" // PiP bottom-left
  | "pip-tr" // PiP top-right
  | "pip-tl" // PiP top-left
  | "split-h" // Split horizontal 50/50
  | "split-v" // Split vertical 50/50
  | "focus-l" // Focus left (70/30)
  | "focus-r" // Focus right (30/70)
  | "grid" // Auto grid (2x2, 3x3, etc based on source count)
  | "stack" // Vertical stack
  // 3-source layouts
  | "pip-dual-br" // Main + 2 PiPs bottom-right
  | "pip-dual-bl" // Main + 2 PiPs bottom-left
  | "split-pip-l" // Split + PiP on left side
  | "split-pip-r" // Split + PiP on right side
  // Featured layout (1 big, rest in strip)
  | "featured" // Main source large, others in bottom strip
  | "featured-r" // Main source large, others in right strip
  // Legacy aliases
  | "fullscreen" // Alias for 'solo'
  | "pip" // Alias for 'pip-br'
  | "side-by-side"; // Alias for 'split-h'

// Legacy types for compatibility
export type PipPosition = "top-left" | "top-right" | "bottom-left" | "bottom-right";
export type SplitDirection = "horizontal" | "vertical";

// How sources are scaled to fit their layer bounds
export type ScalingMode =
  | "stretch" // Stretch to fill (may distort)
  | "letterbox" // Fit with black bars (preserve aspect)
  | "crop"; // Fill and crop overflow (preserve aspect)

export interface LayoutConfig {
  mode: LayoutMode;
  // Scaling mode for all layers
  scalingMode?: ScalingMode;
  // PiP scale (for pip-* modes)
  pipScale?: number; // 0.2 = 20% of canvas (default 0.25)
  // Legacy options (for backwards compatibility)
  pipPosition?: PipPosition;
  splitRatio?: number;
  splitDirection?: SplitDirection;
}

// ============================================================================
// Transition Types (Phase 3 - Compositor)
// ============================================================================

export type TransitionType =
  | "cut"
  | "fade"
  | "slide-left"
  | "slide-right"
  | "slide-up"
  | "slide-down";

export type EasingType = "linear" | "ease-in" | "ease-out" | "ease-in-out";

export interface TransitionConfig {
  type: TransitionType;
  durationMs: number; // 300-1000ms typical
  easing: EasingType;
}

export interface TransitionState {
  active: boolean;
  type: TransitionType;
  progress: number; // 0-1
  fromSceneId: string;
  toSceneId: string;
  startTime: number;
  durationMs: number;
}

// ============================================================================
// Renderer Types (Phase 3 - Compositor)
// ============================================================================

export type RendererType = "canvas2d" | "webgl" | "webgpu" | "auto";

export interface RendererStats {
  fps: number;
  frameTimeMs: number;
  gpuMemoryMB?: number;
}

export type FilterType = "blur" | "colorMatrix" | "glow" | "grayscale" | "sepia" | "invert";

export interface FilterConfig {
  type: FilterType;
  strength?: number;
  brightness?: number;
  contrast?: number;
  saturation?: number;
  hue?: number;
}

// ============================================================================
// Compositor Configuration (Phase 3)
// ============================================================================

export interface CompositorConfig {
  enabled: boolean;
  width: number; // output resolution
  height: number;
  frameRate: number; // 30 or 60
  renderer: RendererType;
  defaultTransition: TransitionConfig;
}

// Default compositor configuration
export const DEFAULT_COMPOSITOR_CONFIG: CompositorConfig = {
  enabled: false,
  width: 1920,
  height: 1080,
  frameRate: 30,
  renderer: "auto",
  defaultTransition: {
    type: "fade",
    durationMs: 500,
    easing: "ease-in-out",
  },
};

// Default layer transform
export const DEFAULT_LAYER_TRANSFORM: LayerTransform = {
  x: 0,
  y: 0,
  width: 1,
  height: 1,
  opacity: 1,
  rotation: 0,
  borderRadius: 0,
  crop: { top: 0, right: 0, bottom: 0, left: 0 },
};

// ============================================================================
// Compositor Worker Messages (Phase 3)
// ============================================================================

/**
 * Layout transition animation configuration
 */
export interface LayoutTransitionConfig {
  durationMs: number;
  easing: EasingType;
}

export type CompositorMainToWorker =
  | { type: "init"; config: CompositorConfig; canvas: OffscreenCanvas }
  | { type: "updateScene"; scene: Scene }
  | { type: "sourceFrame"; sourceId: string; frame: VideoFrame }
  | { type: "sourceImage"; sourceId: string; bitmap: ImageBitmap }
  | { type: "startTransition"; transition: TransitionConfig; toSceneId: string }
  | { type: "updateLayout"; layout: LayoutConfig }
  | { type: "animateLayout"; targetScene: Scene; transition: LayoutTransitionConfig }
  | { type: "resize"; width: number; height: number; frameRate?: number }
  | { type: "setRenderer"; renderer: RendererType }
  | { type: "applyFilter"; layerId: string; filter: FilterConfig }
  | { type: "destroy" };

export type CompositorWorkerToMain =
  | { type: "ready" }
  | { type: "stats"; stats: RendererStats }
  | { type: "transitionComplete"; sceneId: string }
  | { type: "layoutAnimationComplete" }
  | { type: "rendererChanged"; renderer: RendererType }
  | { type: "error"; message: string };

// ============================================================================
// Scene Manager Events (Phase 3)
// ============================================================================

export interface SceneManagerEvents {
  sceneCreated: { scene: Scene };
  sceneDeleted: { sceneId: string };
  sceneActivated: { scene: Scene; previousSceneId: string | null };
  layerAdded: { sceneId: string; layer: Layer };
  layerRemoved: { sceneId: string; layerId: string };
  layerUpdated: { sceneId: string; layer: Layer };
  transitionStarted: { fromSceneId: string; toSceneId: string; transition: TransitionConfig };
  transitionCompleted: { sceneId: string };
  layoutAnimationStarted: { layout: LayoutConfig };
  layoutAnimationCompleted: { layout: LayoutConfig };
  rendererChanged: { renderer: RendererType };
  statsUpdate: { stats: RendererStats };
  error: { message: string; error?: Error };
}

// ============================================================================
// Ingest Endpoint Resolution Types (Gateway Integration)
// ============================================================================

export interface IngestEndpoint {
  nodeId: string;
  baseUrl: string;
  whipUrl?: string;
  rtmpUrl?: string;
  srtUrl?: string;
  region?: string;
  loadScore?: number;
}

export interface IngestMetadata {
  streamId: string;
  streamKey: string;
  tenantId: string;
  recordingEnabled: boolean;
}

export interface IngestEndpoints {
  primary: IngestEndpoint;
  fallbacks: IngestEndpoint[];
  metadata?: IngestMetadata;
}

export interface IngestClientConfig {
  gatewayUrl: string;
  streamKey: string;
  authToken?: string;
  maxRetries?: number;
  initialDelayMs?: number;
}

export type IngestClientStatus = "idle" | "loading" | "ready" | "error";

export interface IngestClientEvents {
  statusChange: { status: IngestClientStatus; error?: string };
  endpointsResolved: { endpoints: IngestEndpoints };
}
