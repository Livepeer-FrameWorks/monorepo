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
  createMultiCodecEncoderConfig,
  DEFAULT_VIDEO_SETTINGS,
  DEFAULT_AUDIO_SETTINGS,
  type EncodedVideoChunkData,
  type EncodedAudioChunkData,
  type VideoCodecFamily,
} from "./core/EncoderManager";
export {
  selectH264Codec,
  selectVP9Codec,
  selectAV1Codec,
  selectCodecString,
  createEncoderConfig as createCodecEncoderConfig,
  getDefaultVideoSettings,
  getDefaultAudioSettings,
  getKeyframeInterval,
  detectEncoderCapabilities,
  mimeToCodecFamily,
  type CodecCapabilities,
} from "./core/CodecProfiles";

// Recording
export { WebMWriter, type WebMWriterOptions, type WebMTrackConfig } from "./recording/WebMWriter";
export { RecordingManager, type RecordingManagerOptions } from "./recording/RecordingManager";

// Audio processing
export { HighPassFilter, type HighPassFilterOptions } from "./audio/HighPassFilter";
export { NoiseGate, type NoiseGateOptions } from "./audio/NoiseGate";
export { ThreeBandEQ, type ThreeBandEQOptions } from "./audio/ThreeBandEQ";
export { DeEsser, type DeEsserOptions } from "./audio/DeEsser";
export { SidechainDucker, type SidechainDuckerOptions } from "./audio/SidechainDucker";

// Bitrate adaptation
export {
  BitrateAdaptation,
  type BitrateAdaptationOptions,
  type CongestionLevel,
} from "./core/BitrateAdaptation";

// Phase 2: Audio mixing and reconnection
export { AudioMixer, type AudioProcessingConfig } from "./core/AudioMixer";
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

// Media file playback
export { MediaFileSource, type MediaFileSourceEvents } from "./core/MediaFileSource";

// Phase 3: Compositor and scene management
export { SceneManager } from "./core/SceneManager";
export { TextOverlaySource, type TextOverlayConfig } from "./core/TextOverlaySource";
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

// Theming
export {
  resolveStudioTheme,
  getAvailableStudioThemes,
  applyStudioTheme,
  applyStudioThemeOverrides,
  clearStudioTheme,
  studioThemeOverridesToStyle,
} from "./StudioThemeManager";
export type { FwThemePreset, StudioThemeOverrides } from "./StudioThemeManager";

// Internationalization
export {
  DEFAULT_STUDIO_TRANSLATIONS,
  getStudioLocalePack,
  getAvailableStudioLocales,
  createStudioTranslator,
  studioTranslate,
} from "./I18n";
export type {
  StudioTranslationStrings,
  StudioTranslateFn,
  StudioLocale,
  StudioI18nConfig,
} from "./I18n";

// Configurable hotkeys
export { DEFAULT_STUDIO_KEY_MAP, buildStudioKeyLookup, matchStudioKey } from "./StudioKeyMap";
export type { StudioKeyMap } from "./StudioKeyMap";

// Vanilla facade
export { createStreamCrafter } from "./vanilla/createStreamCrafter";
export type {
  CreateStreamCrafterConfig,
  StreamCrafterInstance,
} from "./vanilla/createStreamCrafter";

// Reactive state
export { createStudioReactiveState } from "./vanilla/StudioReactiveState";
export type { StudioReactiveState, StudioStateMap } from "./vanilla/StudioReactiveState";
