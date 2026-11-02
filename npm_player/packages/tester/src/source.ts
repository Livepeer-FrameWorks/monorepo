import type { ContentEndpoints, ContentType } from "@livepeer-frameworks/player-core";

/** Protocols the tester lets a viewer pick. "auto" lets the player choose. */
export const PLAY_PROTOCOLS = ["auto", "HLS", "DASH", "WHEP", "MP4", "WEBM"] as const;
export type PlayProtocol = (typeof PLAY_PROTOCOLS)[number];

/** Mist source types the player's forceType option understands, per protocol. */
const PROTOCOL_TO_MIME: Record<Exclude<PlayProtocol, "auto">, string> = {
  HLS: "html5/application/vnd.apple.mpegurl",
  DASH: "dash/video/mp4",
  WHEP: "whep",
  MP4: "html5/video/mp4",
  WEBM: "html5/video/webm",
};

export type PlaySource =
  | { mode: "url"; url: string; protocol?: PlayProtocol }
  | { mode: "mist"; baseUrl: string; streamName: string; protocol?: PlayProtocol }
  | { mode: "playback"; playbackId: string; protocol?: PlayProtocol };

export type PlayMode = PlaySource["mode"];

/** What the tester hands to <Player>. */
export interface PlayerInput {
  contentId: string;
  contentType?: ContentType;
  endpoints?: ContentEndpoints;
  mistUrl?: string;
  gatewayUrl?: string;
  viewerProtocol?: Exclude<PlayProtocol, "auto">;
  forceType?: string;
}

export type Resolved =
  | { input: PlayerInput; source: PlaySource }
  | { error: string; tls?: boolean };

/** Content id for a direct media URL that names no Mist stream. */
export const PASTED_URL_CONTENT_ID = "pasted-url";

const EXTENSION_PROTOCOLS: Array<[RegExp, Exclude<PlayProtocol, "auto">]> = [
  [/\.m3u8$/i, "HLS"],
  [/\.mpd$/i, "DASH"],
  [/\.mp4$/i, "MP4"],
  [/\.webm$/i, "WEBM"],
];

/** Mist output folders that carry the stream name as the next path segment. */
const MIST_FOLDER_PROTOCOLS: Record<string, Exclude<PlayProtocol, "auto"> | null> = {
  hls: "HLS",
  cmaf: null,
  dash: "DASH",
  webrtc: "WHEP",
};

type ParsedUrl =
  | {
      kind: "direct";
      url: string;
      protocol: Exclude<PlayProtocol, "auto">;
      /** A Mist edge and stream name the URL's path points at, to confirm by lookup. */
      mist?: { baseUrl: string; streamName: string };
    }
  | { kind: "mist"; baseUrl: string; streamName: string };

const toUrl = (input: string): URL | { error: string } => {
  let url: URL;
  try {
    url = new URL(input.trim());
  } catch {
    return { error: "Paste a complete URL, starting with https://." };
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    return { error: "Use an http:// or https:// URL." };
  }
  return url;
};

/** Browsers block http:// media on an https:// page (mixed content). */
export const insecureOnSecurePage = (url: string, pageProtocol: string): boolean =>
  pageProtocol === "https:" && /^http:/i.test(url.trim());

const MIXED_CONTENT_ERROR =
  "This page is served over HTTPS, so the browser blocks http:// media. The server needs HTTPS.";

const joinBase = (origin: string, segments: string[]) =>
  segments.length ? `${origin}/${segments.join("/")}` : origin;

const decode = (segment: string) => {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
};

/**
 * Read a pasted playback URL: a direct media or WHEP URL, a Mist output URL
 * (/hls/<name>/..., /cmaf/<name>/..., /dash/<name>/..., /webrtc/<name>, /<name>.mp4),
 * or a Mist stream page (/<name>, /<name>.html, /json_<name>.js, /embed_<name>.js).
 */
export function parsePlaybackUrl(
  input: string,
  protocolHint?: PlayProtocol
): ParsedUrl | { error: string } {
  const url = toUrl(input);
  if ("error" in url) return url;

  const segments = url.pathname.split("/").filter(Boolean);
  const last = segments.at(-1);
  if (!last) {
    return { error: "Add the stream name to the URL, like https://host:8080/my-stream." };
  }

  const href = url.toString();
  const hint = protocolHint && protocolHint !== "auto" ? protocolHint : undefined;
  const extension = EXTENSION_PROTOCOLS.find(([pattern]) => pattern.test(url.pathname))?.[1];

  for (let i = segments.length - 2; i >= 0; i--) {
    const folder = segments[i].toLowerCase();
    if (!(folder in MIST_FOLDER_PROTOCOLS)) continue;
    const isWebrtc = folder === "webrtc";
    if (isWebrtc ? i !== segments.length - 2 : i > segments.length - 3) continue;
    const protocol = hint ?? MIST_FOLDER_PROTOCOLS[folder] ?? extension;
    if (!protocol) break;
    return {
      kind: "direct",
      url: href,
      protocol,
      mist: {
        baseUrl: joinBase(url.origin, segments.slice(0, i)),
        streamName: decode(segments[i + 1]),
      },
    };
  }

  const prefix = joinBase(url.origin, segments.slice(0, -1));
  if (extension === "MP4" || extension === "WEBM") {
    return {
      kind: "direct",
      url: href,
      protocol: hint ?? extension,
      mist: { baseUrl: prefix, streamName: decode(last.replace(/\.(mp4|webm)$/i, "")) },
    };
  }
  if (extension || hint) {
    return { kind: "direct", url: href, protocol: hint ?? extension! };
  }

  const streamName = last
    .replace(/\.html?$/i, "")
    .replace(/^json_(.+)\.js$/i, "$1")
    .replace(/^embed_(.+)\.js$/i, "$1");
  return { kind: "mist", baseUrl: prefix, streamName: decode(streamName) };
}

/** Validate a WHIP URL for the publish tab. */
export function parseWhipUrl(
  input: string,
  pageProtocol = "https:"
): { whipUrl: string } | { error: string; tls?: boolean } {
  const url = toUrl(input);
  if ("error" in url) return url;
  if (insecureOnSecurePage(url.toString(), pageProtocol)) {
    return { error: MIXED_CONTENT_ERROR, tls: true };
  }
  return { whipUrl: url.toString() };
}

/** Normalise a Mist base URL: add http:// when the scheme is missing, drop trailing slashes. */
export function normalizeBaseUrl(input: string): string {
  const trimmed = input.trim();
  const withScheme = /^https?:\/\//i.test(trimmed)
    ? trimmed
    : `http://${trimmed.replace(/^\/+/, "")}`;
  return withScheme.replace(/\/+$/, "");
}

/**
 * Ask a host for json_<name>.js. Any Mist reply counts, including an error
 * status, because then the player can resolve and poll the stream through Mist.
 */
export async function probeMist(
  baseUrl: string,
  streamName: string,
  fetchImpl: typeof fetch = fetch
): Promise<boolean> {
  try {
    const response = await fetchImpl(`${baseUrl}/json_${encodeURIComponent(streamName)}.js`, {
      cache: "no-store",
    });
    if (!response.ok) return false;
    let text = await response.text();
    const jsonp = text.match(/^[^(]+\(([\s\S]*)\);?\s*$/);
    if (jsonp) text = jsonp[1];
    const data = JSON.parse(text);
    return (
      !!data &&
      typeof data === "object" &&
      (typeof data.error === "string" || Array.isArray(data.source))
    );
  } catch {
    return false;
  }
}

const forceTypeFor = (protocol?: PlayProtocol) =>
  protocol && protocol !== "auto" ? PROTOCOL_TO_MIME[protocol] : undefined;

const directEndpoints = (
  url: string,
  protocol: Exclude<PlayProtocol, "auto">,
  nodeId: string
): ContentEndpoints => ({
  primary: { nodeId, protocol, url, outputs: { [protocol]: { protocol, url } } },
  fallbacks: [],
});

export interface ResolveOptions {
  /** window.location.protocol of the page the tester runs on. */
  pageProtocol: string;
  /** Gateway GraphQL URL; required for playback IDs. */
  gatewayUrl?: string;
  fetchImpl?: typeof fetch;
}

/**
 * Turn a tester source into <Player> input. A URL that points into Mist is
 * confirmed with a json_<name>.js lookup, so Mist plays it in Mist mode with the
 * stream's real name while any other host plays the URL as plain media.
 */
export async function resolvePlaySource(
  source: PlaySource,
  { pageProtocol, gatewayUrl, fetchImpl = fetch }: ResolveOptions
): Promise<Resolved> {
  if (source.mode === "playback") {
    const playbackId = source.playbackId.trim();
    if (!playbackId) return { error: "Enter a playback ID." };
    if (!gatewayUrl) return { error: "Playback IDs need a FrameWorks gateway URL." };
    const protocol = source.protocol && source.protocol !== "auto" ? source.protocol : undefined;
    return {
      source: { ...source, playbackId },
      input: { contentId: playbackId, gatewayUrl, viewerProtocol: protocol },
    };
  }

  if (source.mode === "mist") {
    const streamName = source.streamName.trim();
    if (!source.baseUrl.trim()) return { error: "Enter the MistServer base URL." };
    if (!streamName) return { error: "Enter the stream name." };
    const baseUrl = normalizeBaseUrl(source.baseUrl);
    if (insecureOnSecurePage(baseUrl, pageProtocol))
      return { error: MIXED_CONTENT_ERROR, tls: true };
    return {
      source: { ...source, baseUrl, streamName },
      input: { contentId: streamName, mistUrl: baseUrl, forceType: forceTypeFor(source.protocol) },
    };
  }

  const parsed = parsePlaybackUrl(source.url, source.protocol);
  if ("error" in parsed) return parsed;
  if (insecureOnSecurePage(source.url, pageProtocol))
    return { error: MIXED_CONTENT_ERROR, tls: true };

  if (parsed.kind === "mist") {
    if (!(await probeMist(parsed.baseUrl, parsed.streamName, fetchImpl))) {
      return {
        error: `No MistServer stream answered at ${parsed.baseUrl}. For a media file or manifest, pick its protocol.`,
      };
    }
    return {
      source,
      input: {
        contentId: parsed.streamName,
        mistUrl: parsed.baseUrl,
        forceType: forceTypeFor(source.protocol),
      },
    };
  }

  if (parsed.mist && (await probeMist(parsed.mist.baseUrl, parsed.mist.streamName, fetchImpl))) {
    return {
      source,
      input: {
        contentId: parsed.mist.streamName,
        mistUrl: parsed.mist.baseUrl,
        forceType: forceTypeFor(parsed.protocol),
      },
    };
  }

  // HLS, DASH and progressive files can be live or on demand; the player reads
  // that from the media. WHEP is always live.
  const contentId = parsed.mist?.streamName ?? PASTED_URL_CONTENT_ID;
  return {
    source,
    input: {
      contentId,
      contentType: parsed.protocol === "WHEP" ? "live" : undefined,
      endpoints: directEndpoints(parsed.url, parsed.protocol, contentId),
    },
  };
}

/** Ingest URIs a stock MistServer exposes for a stream (default ports). */
export function mistIngestUris(baseUrl: string, streamName: string) {
  const base = normalizeBaseUrl(baseUrl);
  let host = "localhost";
  try {
    host = new URL(base).hostname;
  } catch {
    // keep localhost for an unparsable base
  }
  return {
    rtmp: `rtmp://${host}:1935/live/${streamName}`,
    srt: `srt://${host}:8889?streamid=${streamName}`,
    whip: `${base}/webrtc/${encodeURIComponent(streamName)}`,
  };
}

const isProtocol = (value: string | null): value is PlayProtocol =>
  !!value && (PLAY_PROTOCOLS as readonly string[]).includes(value);

/**
 * Whether a URL is an ingest endpoint, whose path carries a stream key: any /whip path,
 * the Foghorn front door (/ingest/<key>), or /webrtc/<key> on an ingest host such as
 * the dashboard's edge-ingest name. Mist serves WebRTC playback on /webrtc/ as well,
 * so that path alone is not enough.
 */
function looksLikeIngest(url: string): boolean {
  if (/\/whip(\/|$|\?)/i.test(url)) return true;
  let parsed: URL;
  try {
    parsed = new URL(url.trim());
  } catch {
    return false;
  }
  if (/(^|\/)ingest\//i.test(parsed.pathname)) return true;
  const firstLabel = parsed.hostname.split(".")[0] ?? "";
  return /ingest/i.test(firstLabel) && /\/webrtc\//i.test(parsed.pathname);
}

const mistSourceUrl = (baseUrl: string, streamName: string) =>
  `${normalizeBaseUrl(baseUrl)}/${encodeURIComponent(streamName)}`;

/** Whether a play source may go into a shared link or the browser's storage. */
export function isPersistable(source: PlaySource): boolean {
  if (source.mode === "playback") return true;
  if (source.mode === "mist") {
    // A stream name on an ingest host is its stream key; test the WHIP URL it forms there.
    return !looksLikeIngest(mistIngestUris(source.baseUrl, source.streamName).whip);
  }
  return !looksLikeIngest(source.url);
}

/**
 * Query string that reloads this play source: ?url= (with ?protocol=) or ?id=.
 * Returns null for sources that must not be shared.
 */
export function shareQuery(source: PlaySource): string | null {
  if (!isPersistable(source)) return null;
  const params = new URLSearchParams();
  if (source.mode === "playback") {
    params.set("id", source.playbackId);
  } else if (source.mode === "mist") {
    params.set("url", mistSourceUrl(source.baseUrl, source.streamName));
  } else {
    params.set("url", source.url.trim());
  }
  if (source.protocol && source.protocol !== "auto") params.set("protocol", source.protocol);
  return params.toString();
}

/** Read a shared play source from a page's query string. */
export function readShareQuery(search: string): PlaySource | null {
  const params = new URLSearchParams(search);
  const protocolParam = params.get("protocol");
  const protocol = isProtocol(protocolParam) ? protocolParam : undefined;
  const id = params.get("id")?.trim();
  if (id) return { mode: "playback", playbackId: id, protocol };
  const url = params.get("url")?.trim();
  if (url && !looksLikeIngest(url)) return { mode: "url", url, protocol };
  return null;
}
