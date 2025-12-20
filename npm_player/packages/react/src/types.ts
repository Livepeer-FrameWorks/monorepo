/**
 * React-specific types for FrameWorks player
 */
import type React from 'react';
import type { 
  PlayerOptions, 
  PlayerState, 
  PlayerStateContext, 
  ContentEndpoints,
  MetaTrackSubscription,
  PlaybackQuality,
  QualityThresholds,
  ContentType
} from '@livepeer-frameworks/player-core';

export interface PlayerProps {
  /** Content identifier or stream name */
  contentId: string;
  /** Content type */
  contentType: ContentType;
  /** Pre-resolved endpoints/capabilities from Gateway/Foghorn */
  endpoints?: ContentEndpoints;
  /** Optional thumbnail/poster image */
  thumbnailUrl?: string | null;
  /** Unified options (branding, playback prefs, etc.) */
  options?: Partial<PlayerOptions>;
  /** Detailed state updates for UI (booting, gateway, connecting, playing, etc.) */
  onStateChange?: (state: PlayerState, context?: PlayerStateContext) => void;
}

export interface MistPlayerProps {
  streamName: string;
  htmlUrl?: string;
  playerJsUrl?: string;
  developmentMode?: boolean;
  muted?: boolean;
  poster?: string;
}

export interface LoadingScreenProps {
  message?: string;
}

export interface ThumbnailOverlayProps {
  thumbnailUrl?: string | null | undefined;
  onPlay?: () => void;
  message?: string | null;
  showUnmuteMessage?: boolean;
  style?: React.CSSProperties;
  className?: string;
}

export interface AnimatedBubbleProps {
  index: number;
}

export interface CenterLogoProps {
  containerRef: React.RefObject<HTMLDivElement>;
  scale?: number;
  onHitmarker?: (event: React.MouseEvent) => void;
}

export interface HitmarkerData {
  id: number;
  x: number;
  y: number;
}

export interface DvdLogoProps {
  parentRef: React.RefObject<HTMLDivElement>;
  scale?: number;
}

// React-specific hook option types
export interface UsePlaybackQualityOptions {
  videoElement: HTMLVideoElement | null;
  enabled?: boolean;
  sampleInterval?: number;
  thresholds?: Partial<QualityThresholds>;
  onQualityDegraded?: (quality: PlaybackQuality) => void;
}

export interface UseMetaTrackOptions {
  mistBaseUrl: string;
  streamName: string;
  enabled?: boolean;
  subscriptions?: MetaTrackSubscription[];
}

export interface UseStreamStateOptions {
  /** MistServer base URL */
  mistBaseUrl: string;
  /** Stream name */
  streamName: string;
  /** Poll interval in ms (default: 3000) */
  pollInterval?: number;
  /** Enable/disable the hook (default: true) */
  enabled?: boolean;
  /** Use WebSocket instead of HTTP polling (default: true) */
  useWebSocket?: boolean;
  /** Enable debug logging (default: false) */
  debug?: boolean;
}

export interface UseTelemetryOptions {
  enabled?: boolean;
  endpoint?: string;
  authToken?: string;
  interval?: number;
  batchSize?: number;
  contentId?: string;
  contentType?: string;
  playerType?: string;
  protocol?: string;
}

// Re-export core types for convenience
export type {
  PlayerState,
  PlayerStateContext,
  ContentEndpoints,
  PlayerOptions,
  PlaybackQuality,
  TelemetryOptions,
  TelemetryPayload,
  MetaTrackEvent,
  MetaTrackEventType,
  SubtitleCue,
  PlaybackMode,
  MistStreamInfo,
  StreamState,
  StreamStatus,
  EndpointInfo,
  ContentMetadata,
} from '@livepeer-frameworks/player-core';
