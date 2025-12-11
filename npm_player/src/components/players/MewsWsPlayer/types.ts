/**
 * MEWS WebSocket Player Types
 */

export interface MewsMessage {
  type: string;
  data?: any;
}

export interface CodecDataMessage extends MewsMessage {
  type: 'codec_data';
  data: {
    codecs: string[];
  };
}

export interface OnTimeMessage extends MewsMessage {
  type: 'on_time';
  data: {
    current: number;
    end?: number;
  };
}

export interface TracksMessage extends MewsMessage {
  type: 'tracks';
  data: {
    codecs: string[];
    current?: number;
  };
}

export interface MewsCommand {
  type: 'request_codec_data' | 'play' | 'hold' | 'seek' | 'set_speed' | 'tracks';
  [key: string]: any;
}

export interface RequestCodecDataCommand extends MewsCommand {
  type: 'request_codec_data';
  supported_codecs: string[];
}

export interface SeekCommand extends MewsCommand {
  type: 'seek';
  seek_time: number;
}

export interface SetSpeedCommand extends MewsCommand {
  type: 'set_speed';
  play_rate: number | 'auto';
}

export interface TracksCommand extends MewsCommand {
  type: 'tracks';
  video?: string;
  subtitle?: string; // Track index or 'none'
}

export interface WebSocketManagerOptions {
  url: string;
  maxReconnectAttempts?: number;
  onMessage: (data: ArrayBuffer | string) => void;
  onOpen: () => void;
  onClose: () => void;
  onError: (message: string) => void;
}

export interface SourceBufferManagerOptions {
  mediaSource: MediaSource;
  videoElement: HTMLVideoElement;
  onError: (message: string) => void;
}

export interface AnalyticsConfig {
  enabled: boolean;
  endpoint: string | null;
}
