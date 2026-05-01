export type MistPlayRate = number | "auto" | "fast-forward";

export interface MistOnTime {
  current: number;
  end: number;
  begin: number;
  total?: number;
  tracks?: string[] | number[];
  jitter?: number;
  play_rate_curr?: MistPlayRate;
  paused?: boolean;
  live_point?: boolean;
}

export type MistEvent =
  | { type: "metadata"; time: number; track: string | number; data: unknown; duration?: number }
  | { type: "codec_data"; codecs?: string[]; tracks?: number[]; current?: number }
  | { type: "info"; meta?: unknown; type_?: string }
  | ({ type: "on_time" } & MistOnTime)
  | { type: "tracks"; tracks?: unknown; codecs?: string[]; current?: number }
  | {
      type: "set_speed";
      play_rate?: number | "auto";
      play_rate_curr?: MistPlayRate;
      play_rate_prev?: MistPlayRate;
    }
  | { type: "pause"; paused?: boolean; reason?: string; begin?: number; end?: number }
  | { type: "seek"; live_point?: boolean }
  | { type: "on_stop" }
  | { type: "error"; message?: string }
  | { type: "on_error"; message?: string }
  | { type: "on_answer_sdp"; result?: boolean; answer_sdp?: string }
  | { type: "on_connected" }
  | { type: "on_disconnected"; code?: number };

export type MistCommand =
  | { type: "play"; seek_time?: number | "live"; ff_add?: number }
  | { type: "hold" }
  | { type: "stop" }
  | { type: "seek"; seek_time: number | "live"; ff_add?: number }
  | { type: "set_speed"; play_rate: number | "auto" }
  | { type: "fast_forward"; ff_add: number }
  | { type: "request_codec_data"; supported_combinations?: string[][][] }
  | { type: "tracks"; video?: string; audio?: string; subtitle?: string }
  | { type: "offer_sdp"; offer_sdp: string };

export type MistMetadataCommand =
  | { type: "play"; seek_time?: number | "live"; ff_to?: number }
  | { type: "hold" }
  | { type: "stop" }
  | { type: "seek"; seek_time: number | "live"; ff_to?: number }
  | { type: "fast_forward"; ff_to: number }
  | { type: "set_speed"; play_rate: number | "auto" }
  | { type: "tracks"; meta: string };

export const ROUND_TRIPPED_COMMANDS = ["seek", "set_speed", "request_codec_data"] as const;
export type RoundTrippedCommand = (typeof ROUND_TRIPPED_COMMANDS)[number];
