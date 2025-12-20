/**
 * StreamCrafter Svelte
 * Svelte 5 stores and components for browser-based streaming
 */

// Components (self-contained, like Player)
export { default as StreamCrafter } from './StreamCrafter.svelte';

// Compositor Components
export { default as CompositorControls } from './components/CompositorControls.svelte';
export { default as SceneSwitcher } from './components/SceneSwitcher.svelte';
export { default as LayerList } from './components/LayerList.svelte';

// Advanced Panel (dev mode sidebar)
export { default as AdvancedPanel, type AudioProcessingSettings } from './components/AdvancedPanel.svelte';

// Context
export { default as StreamCrafterProvider } from './context/StreamCrafterProvider.svelte';

// Device/Screen/Stats stores
export {
  createDevicesStore,
  type DevicesState,
  type DevicesStore,
} from './stores/devices.svelte';

export {
  createScreenCaptureStore,
  type ScreenCaptureState,
  type ScreenCaptureStore,
} from './stores/screenCapture.svelte';

export {
  createStreamStatsStore,
  type StreamStatsState,
  type StreamStatsStore,
} from './stores/streamStats.svelte';

// Main store (V2 is now primary, V1 removed)
export {
  createStreamCrafterContextV2 as createStreamCrafterContext,
  setStreamCrafterContextV2 as setStreamCrafterContext,
  getStreamCrafterContextV2 as getStreamCrafterContext,
  type StreamCrafterV2State as StreamCrafterState,
  type StreamCrafterContextV2Store as StreamCrafterContextStore,
} from './stores/streamCrafterContextV2';
// Alias for backwards compat
export {
  createStreamCrafterContextV2,
  setStreamCrafterContextV2,
  getStreamCrafterContextV2,
  type StreamCrafterV2State,
  type StreamCrafterContextV2Store,
} from './stores/streamCrafterContextV2';

// Audio Stores - using .svelte.ts for rune support
export {
  createAudioLevelsStore,
  type AudioLevelsState,
  type AudioLevelsStore,
} from './stores/audioLevels.svelte';

// Compositor Store (Phase 3)
export {
  createCompositorStore,
  type CompositorState,
  type CompositorStore,
  type CreateCompositorStoreOptions,
} from './stores/compositor';

// Gateway Integration Store (Phase 3.5)
export {
  createIngestEndpointsStore,
  getIngestEndpointsStore,
  type IngestEndpointsOptions,
  type IngestEndpointsState,
  type IngestEndpointsStore,
} from './stores/ingestEndpoints';

// Re-export types from core
export type {
  IngestState,
  IngestStateContext,
  IngestStateContextV2,
  IngestStats,
  IngestControllerConfig,
  IngestControllerConfigV2,
  CaptureOptions,
  ScreenCaptureOptions,
  DeviceInfo,
  QualityProfile,
  WhipConnectionState,
  MediaSource,
  SourceType,
  ReconnectionState,
  ReconnectionConfig,
  // Compositor types (Phase 3)
  Scene,
  Layer,
  LayerTransform,
  LayoutConfig,
  LayoutMode,
  TransitionConfig,
  TransitionType,
  RendererType,
  RendererStats,
  CompositorConfig,
} from '@livepeer-frameworks/streamcrafter-core';

// Re-export IngestClient from core for direct usage
export { IngestClient } from '@livepeer-frameworks/streamcrafter-core';
