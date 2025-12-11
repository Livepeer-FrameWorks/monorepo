import type { ContentEndpoints } from "@livepeer-frameworks/player";

export type PlayerMode = "sandbox" | "override";

export type MockStream = {
  id: string;
  label: string;
  description: string;
  endpoints: ContentEndpoints;
  contentId: string;
  contentType: "live" | "dvr" | "clip";
};

export type MistEndpointId =
  | "whep"
  | "whip"
  | "hls"
  | "dash"
  | "mp4"
  | "mistHtml"
  | "playerJs"
  | "rtmp"
  | "srt";

export type MistSettings = {
  baseUrl: string;
  viewerPath: string;
  streamName: string;
  label: string;
  authToken?: string;
  ingestApp?: string;
  ingestUser?: string;
  ingestPassword?: string;
};

export type MistProfile = {
  id: string;
  name: string;
  settings: MistSettings;
  overrides: Partial<Record<MistEndpointId, string>>;
};

export type MistEndpointDefinition = {
  id: MistEndpointId;
  label: string;
  category: "ingest" | "playback";
  hint?: string;
  build: (ctx: MistContext) => string;
};

export type MistContext = {
  base: string;
  apiBase: string;
  viewerBase: string;
  streamName: string;
  authToken?: string;
  host?: string;
  ingestApp?: string;
};

export type EndpointStatus = "idle" | "checking" | "ok" | "error";
