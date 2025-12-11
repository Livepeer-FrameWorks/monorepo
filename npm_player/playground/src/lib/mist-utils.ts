import type { ContentEndpoints, OutputEndpoint } from "@livepeer-frameworks/player";
import type { MistSettings, MistContext, MistEndpointId, MistProfile } from "./types";
import { DEFAULT_MIST_SETTINGS, MIST_ENDPOINTS, MIST_STORAGE_KEYS } from "./constants";

export function sanitizeBaseUrl(input: string): string {
  const trimmed = input.trim();
  if (!trimmed) return "http://localhost:4242";
  if (!/^https?:\/\//i.test(trimmed)) {
    return `https://${trimmed.replace(/^\/+/, "")}`;
  }
  return trimmed.replace(/\/+$/, "");
}

export function normalizeViewerPath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed || trimmed === "/") return "";
  const withoutLeading = trimmed.replace(/^\/+/, "");
  const withoutTrailing = withoutLeading.replace(/\/+$/, "");
  return withoutTrailing ? `/${withoutTrailing}` : "";
}

export function buildMistContext(settings: MistSettings): MistContext {
  const base = sanitizeBaseUrl(settings.baseUrl || DEFAULT_MIST_SETTINGS.baseUrl);
  const viewerPath = normalizeViewerPath(settings.viewerPath || DEFAULT_MIST_SETTINGS.viewerPath);
  const streamName = settings.streamName.trim() || DEFAULT_MIST_SETTINGS.streamName;
  const viewerBase = `${base}${viewerPath}`;
  const apiBase = `${base}/api`;
  let host: string | undefined;
  try {
    host = new URL(base).host;
  } catch {
    host = undefined;
  }
  return {
    base,
    apiBase,
    viewerBase,
    streamName,
    authToken: settings.authToken?.trim() || undefined,
    host,
    ingestApp: settings.ingestApp?.trim() || undefined
  };
}

export function interpolateTemplate(template: string, ctx: MistContext): string {
  return template
    .replace(/\{stream\}/gi, ctx.streamName)
    .replace(/\{base\}/gi, ctx.base)
    .replace(/\{viewerBase\}/gi, ctx.viewerBase)
    .replace(/\{apiBase\}/gi, ctx.apiBase)
    .replace(/\{host\}/gi, ctx.host ?? "")
    .replace(/\{app\}/gi, ctx.ingestApp ?? "live");
}

export function generateMistEndpointMap(
  ctx: MistContext,
  overrides: Partial<Record<MistEndpointId, string>>
): Record<MistEndpointId, string> {
  return MIST_ENDPOINTS.reduce<Record<MistEndpointId, string>>((acc, def) => {
    const override = overrides[def.id];
    const value = override && override.trim().length ? override : def.build(ctx);
    acc[def.id] = interpolateTemplate(value, ctx);
    return acc;
  }, {} as Record<MistEndpointId, string>);
}

export function buildMistContentEndpoints(
  ctx: MistContext,
  endpoints: Record<MistEndpointId, string>
): ContentEndpoints | null {
  const outputs: Record<string, OutputEndpoint> = {};
  const push = (key: string, id: MistEndpointId) => {
    const url = endpoints[id];
    if (!url) return;
    outputs[key] = {
      protocol: key,
      url
    } as OutputEndpoint;
  };

  push("MIST_HTML", "mistHtml");
  push("PLAYER_JS", "playerJs");
  push("WHEP", "whep");
  push("HLS", "hls");
  push("DASH", "dash");
  push("MP4", "mp4");

  const primaryUrl =
    endpoints.whep ||
    endpoints.hls ||
    endpoints.mistHtml ||
    endpoints.mp4 ||
    endpoints.dash ||
    endpoints.playerJs;

  if (!primaryUrl) {
    return null;
  }

  return {
    primary: {
      nodeId: `mist-${ctx.streamName}`,
      protocol: "custom",
      url: primaryUrl,
      outputs
    },
    fallbacks: []
  };
}

// Local storage utilities
export function loadJson<T>(key: string): T | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(key);
    if (!raw) return null;
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

export function saveJson<T>(key: string, value: T): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // ignore storage quota errors
  }
}

export function generateProfileId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `mist-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

export function getInitialMistState(): {
  settings: MistSettings;
  overrides: Partial<Record<MistEndpointId, string>>;
  profiles: MistProfile[];
  selectedProfile: string | null;
} {
  if (typeof window === "undefined") {
    return {
      settings: DEFAULT_MIST_SETTINGS,
      overrides: {},
      profiles: [],
      selectedProfile: null
    };
  }
  const profiles = loadJson<MistProfile[]>(MIST_STORAGE_KEYS.profiles) ?? [];
  const storedSelected = window.localStorage.getItem(MIST_STORAGE_KEYS.selectedProfile);
  const selectedProfile = storedSelected ? profiles.find((p) => p.id === storedSelected) ?? null : null;
  if (selectedProfile) {
    return {
      settings: selectedProfile.settings,
      overrides: selectedProfile.overrides ?? {},
      profiles,
      selectedProfile: selectedProfile.id
    };
  }
  return {
    settings: loadJson<MistSettings>(MIST_STORAGE_KEYS.settings) ?? DEFAULT_MIST_SETTINGS,
    overrides: loadJson<Partial<Record<MistEndpointId, string>>>(MIST_STORAGE_KEYS.overrides) ?? {},
    profiles,
    selectedProfile: storedSelected && profiles.some((p) => p.id === storedSelected) ? storedSelected : null
  };
}
