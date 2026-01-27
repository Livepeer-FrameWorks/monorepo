/**
 * StreamCrafter Core
 * Framework-agnostic streaming library with WebCodecs support
 */

// Types
export * from "./types";

// Core classes
export { TypedEventEmitter } from "./core/EventEmitter";
export { DeviceManager } from "./core/DeviceManager";
export { ScreenCapture } from "./core/ScreenCapture";
export { WhipClient } from "./core/WhipClient";
// IngestController (V2 is now the primary, V1 removed)
export { IngestControllerV2 as IngestController } from "./core/IngestControllerV2";
export { IngestControllerV2 } from "./core/IngestControllerV2"; // Alias for backwards compat
export {
  EncoderManager,
  createEncoderConfig,
  DEFAULT_VIDEO_SETTINGS,
  DEFAULT_AUDIO_SETTINGS,
  type EncodedVideoChunkData,
  type EncodedAudioChunkData,
} from "./core/EncoderManager";

// Phase 2: Audio mixing and reconnection
export { AudioMixer } from "./core/AudioMixer";
export { ReconnectionManager, DEFAULT_RECONNECTION_CONFIG } from "./core/ReconnectionManager";

// Phase 3.5: Gateway integration (ingest endpoint resolution)
export { IngestClient } from "./core/IngestClient";

// Feature detection
export {
  detectCapabilities,
  isWebCodecsSupported,
  isWebRTCSupported,
  isMediaDevicesSupported,
  isScreenCaptureSupported,
  isRTCRtpScriptTransformSupported,
  isWebCodecsEncodingPathSupported,
  getRecommendedPath,
  isVideoCodecSupported,
  isAudioCodecSupported,
  getSupportedVideoCodecs,
  getSupportedAudioCodecs,
} from "./core/FeatureDetection";

// Media constraints
export {
  getAudioConstraints,
  getVideoConstraints,
  getAvailableProfiles,
  buildMediaConstraints,
  mergeWithCustomConstraints,
  getEncoderSettings,
} from "./core/MediaConstraints";

// Phase 3: Compositor and scene management
export { SceneManager } from "./core/SceneManager";
export {
  TransitionEngine,
  createDefaultTransitionConfig,
  createCutTransition,
  createFadeTransition,
  createSlideTransition,
  getAvailableTransitionTypes,
  getAvailableEasingTypes,
  validateTransitionConfig,
} from "./core/TransitionEngine";

// Layouts
export {
  applyLayout,
  createDefaultLayoutConfig,
  getLayoutPresets,
  getAvailablePresets,
  getMinSourcesForLayout,
  isLayoutAvailable,
  LAYOUT_PRESETS,
  // Legacy exports
  applyFullscreenLayout,
  applyPipLayout,
  applySideBySideLayout,
  createPipLayoutConfig,
  createSideBySideLayoutConfig,
  getAvailableLayoutModes,
} from "./core/layouts";
export type { LayoutPreset } from "./core/layouts";

// Renderers
export {
  createRenderer,
  registerRenderer,
  getSupportedRenderers,
  getRecommendedRenderer,
} from "./core/renderers";
export type { CompositorRenderer } from "./core/renderers";
export { Canvas2DRenderer } from "./core/renderers/Canvas2DRenderer";
export { WebGLRenderer } from "./core/renderers/WebGLRenderer";
export { WebGPURenderer } from "./core/renderers/WebGPURenderer";
