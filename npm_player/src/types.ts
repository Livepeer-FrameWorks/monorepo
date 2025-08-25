import React from 'react';

export interface PlayerProps {
  /** Content identifier or stream name */
  contentId: string;
  /** Content type */
  contentType: 'live' | 'dvr' | 'clip';
  /** Pre-resolved endpoints/capabilities from Gateway/Foghorn */
  endpoints?: ContentEndpoints;
  /** Optional thumbnail/poster image */
  thumbnailUrl?: string | null;
  /** Unified options (branding, playback prefs, etc.) */
  options?: Partial<PlayerOptions>;
}

export interface MistPlayerProps {
  /** Name of the stream to display */
  streamName: string;
  /** Optional direct viewer HTML URL; if provided, iframe is used */
  htmlUrl?: string;
  /** Optional direct player.js URL for embedded mode */
  playerJsUrl?: string;
  /** Whether to use development mode - uses 'dev' skin (default: false) */
  developmentMode?: boolean;
  /** Whether to start muted (default: true) */
  muted?: boolean;
  /** URL to poster/thumbnail image (optional) */
  poster?: string;
}

export interface DirectSourcePlayerProps {
  src: string;
  muted?: boolean;
  controls?: boolean;
  poster?: string;
  onError?: () => void;
}

export interface WHEPPlayerProps {
  /** WHEP endpoint URL */
  whepUrl: string;
  /** Whether to auto-play the stream (default: true) */
  autoPlay?: boolean;
  /** Whether to start muted (default: true) */
  muted?: boolean;
  /** Callback function for error events */
  onError?: (error: Error) => void;
  /** Callback function when connection is established */
  onConnected?: () => void;
  /** Callback function when connection is lost */
  onDisconnected?: () => void;
}

export interface LoadingScreenProps {
  /** Loading message to display (default: "Waiting for source...") */
  message?: string;
}

export interface ThumbnailOverlayProps {
  /** URL to thumbnail image (optional) */
  thumbnailUrl?: string | null | undefined;
  /** Callback when user clicks to play */
  onPlay?: () => void;
  /** Message to display (used instead of streamName) */
  message?: string | null;
  /** Whether to show unmute message instead of play button (default: false) */
  showUnmuteMessage?: boolean;
  /** Additional styles for the overlay */
  style?: React.CSSProperties;
}

// LoadingScreen internal component types
export interface AnimatedBubbleProps {
  /** Index for staggered animation timing */
  index: number;
}

export interface CenterLogoProps {
  /** Reference to container element */
  containerRef: React.RefObject<HTMLDivElement>;
  /** Scale factor for logo size (default: 0.2) */
  scale?: number;
  /** Callback when logo is clicked */
  onHitmarker?: (event: React.MouseEvent) => void;
}

export interface HitmarkerData {
  /** Unique identifier */
  id: number;
  /** X position */
  x: number;
  /** Y position */
  y: number;
}

export interface DvdLogoProps {
  /** Reference to parent container element */
  parentRef: React.RefObject<HTMLDivElement>;
  /** Scale factor for logo size (default: 0.15) */
  scale?: number;
} 

// FrameWorks Player options
export interface PlayerOptions {
  gatewayUrl?: string;
  authToken?: string;
  autoplay?: boolean;
  muted?: boolean;
  controls?: boolean;
  preferredProtocol?: 'auto' | 'whep' | 'mist' | 'native';
  analytics?: {
    enabled: boolean;
    endpoint?: string;
    sessionTracking: boolean;
  };
  branding?: {
    logoUrl?: string;
    showLogo?: boolean;
    position?: 'top-left' | 'top-right' | 'bottom-left' | 'bottom-right';
    width?: number;
    height?: number;
    clickUrl?: string;
  };
  debug?: boolean;
  verboseLogging?: boolean;
}

// To-be Gateway/Foghorn viewer resolution types
export type StreamProtocol = 'WHEP' | 'HLS' | 'DASH' | 'MP4' | 'WEBM' | 'RTMP' | 'MIST_HTML';

export interface OutputCapabilities {
  supportsSeek: boolean;
  supportsQualitySwitch: boolean;
  maxBitrate?: number;
  hasAudio: boolean;
  hasVideo: boolean;
  codecs?: string[];
}

export interface QualityLevel {
  id: string;
  label: string;
  width?: number;
  height?: number;
  bitrate?: number;
}

export interface OutputEndpoint {
  protocol: StreamProtocol | string;
  url: string;
  capabilities?: OutputCapabilities;
}

export interface EndpointInfo {
  nodeId: string;
  baseUrl?: string;
  protocol: StreamProtocol | string;
  url: string;
  quality?: QualityLevel;
  capabilities?: OutputCapabilities;
  outputs?: Record<string, OutputEndpoint>;
}

export interface ContentMetadata {
  title?: string;
  description?: string;
  duration?: number;
  thumbnailUrl?: string;
  createdAt?: string;
  status?: 'AVAILABLE' | 'PROCESSING' | 'ERROR' | 'OFFLINE';
  viewCount?: number;
  isLive?: boolean;
  recordingSize?: number;
  clipSource?: string;
}

export interface ContentEndpoints {
  primary: EndpointInfo;
  fallbacks: EndpointInfo[];
  metadata?: ContentMetadata;
}