/**
 * StreamCrafter React
 * React hooks and components for browser-based streaming
 */

// Components (self-contained, like Player)
export { StreamCrafter, default as StreamCrafterComponent, type StreamCrafterProps } from './components/StreamCrafter';

// Compositor Components (Phase 3)
export { SceneSwitcher, type SceneSwitcherProps } from './components/SceneSwitcher';
export { LayerList, type LayerListProps } from './components/LayerList';
export { CompositorControls, type CompositorControlsProps } from './components/CompositorControls';

// Hooks
export { useDevices, type UseDevicesReturn } from './hooks/useDevices';
export { useScreenCapture, type UseScreenCaptureReturn } from './hooks/useScreenCapture';
export { useStreamStats, type UseStreamStatsOptions, type UseStreamStatsReturn } from './hooks/useStreamStats';

// Main hook (V2 is now primary, V1 removed)
export { useStreamCrafterV2 as useStreamCrafter, type UseStreamCrafterV2Options as UseStreamCrafterOptions, type UseStreamCrafterV2Return as UseStreamCrafterReturn } from './hooks/useStreamCrafterV2';
export { useStreamCrafterV2, type UseStreamCrafterV2Options, type UseStreamCrafterV2Return } from './hooks/useStreamCrafterV2'; // Alias for backwards compat

// Audio Hooks
export { useAudioLevels, type UseAudioLevelsOptions, type UseAudioLevelsReturn, type AudioLevels } from './hooks/useAudioLevels';

// Compositor Hook (Phase 3)
export { useCompositor, type UseCompositorOptions, type UseCompositorReturn } from './hooks/useCompositor';

// Gateway Integration Hook (Phase 3.5)
export { useIngestEndpoints, type UseIngestEndpointsOptions, type UseIngestEndpointsResult } from './hooks/useIngestEndpoints';

// Context
export { StreamCrafterProvider, useStreamCrafterContext, type StreamCrafterProviderProps } from './context/StreamCrafterContext';

// Re-export types from core
export type {
  IngestState,
  IngestStateContext,
  IngestStateContextV2,
  IngestStats,
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
  // Ingest endpoint types (Phase 3.5)
  IngestEndpoint,
  IngestEndpoints,
  IngestMetadata,
  IngestClientConfig,
  IngestClientStatus,
} from '@livepeer-frameworks/streamcrafter-core';

// Re-export IngestClient from core for direct usage
export { IngestClient } from '@livepeer-frameworks/streamcrafter-core';
