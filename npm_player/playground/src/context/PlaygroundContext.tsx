import { useState, useMemo, useEffect, useCallback, type ReactNode } from "react";
import type { MistSource, IngestUris, PlayerType, ConnectionStatus } from "../lib/types";
import { PlaygroundContext, type PlaygroundContextValue } from "./usePlayground";
import { DEFAULTS, STORAGE_KEYS, POLL_INTERVAL_MS } from "../lib/constants";
import {
  sanitizeBaseUrl,
  buildViewerBase,
  buildIngestUris,
  buildMistJsonUrl,
  loadString,
  saveString,
  extractHostname,
} from "../lib/mist-utils";
import { resolveTheme } from "@livepeer-frameworks/player-core";
import type { FwThemeOverrides, FwThemePreset } from "@livepeer-frameworks/player-core";

// Values for CSS-only themes (defined in CSS files, not in ThemeManager JS)
const CSS_ONLY_OVERRIDES: Record<string, FwThemeOverrides> = {
  light: {
    surfaceDeep: "0 0% 97%",
    surface: "220 10% 94%",
    surfaceRaised: "220 10% 90%",
    surfaceActive: "220 10% 85%",
    text: "220 20% 15%",
    textBright: "220 20% 5%",
    textMuted: "220 10% 45%",
    textFaint: "220 10% 70%",
    accent: "218 80% 50%",
    accentSecondary: "195 80% 42%",
    success: "140 60% 35%",
    danger: "0 70% 50%",
    warning: "35 80% 45%",
  },
  "neutral-dark": {
    surfaceDeep: "0 0% 8%",
    surface: "0 0% 12%",
    surfaceRaised: "0 0% 16%",
    surfaceActive: "0 0% 22%",
    text: "0 0% 82%",
    textBright: "0 0% 95%",
    textMuted: "0 0% 55%",
    textFaint: "0 0% 38%",
    accent: "210 100% 55%",
    accentSecondary: "195 80% 55%",
    success: "140 60% 50%",
    danger: "0 70% 55%",
    warning: "35 80% 55%",
  },
};

// Map theme tokens → playground --tn-* CSS custom properties
const THEME_TO_TN: [keyof FwThemeOverrides, string][] = [
  ["surfaceDeep", "--tn-bg-dark"],
  ["surface", "--tn-bg"],
  ["surfaceRaised", "--tn-bg-highlight"],
  ["surfaceActive", "--tn-bg-visual"],
  ["text", "--tn-fg"],
  ["textMuted", "--tn-fg-dark"],
  ["textFaint", "--tn-fg-gutter"],
  ["textFaint", "--tn-comment"],
  ["surfaceRaised", "--tn-terminal"],
  ["accent", "--tn-blue"],
  ["accentSecondary", "--tn-cyan"],
  ["accentSecondary", "--tn-teal"],
  ["success", "--tn-green"],
  ["warning", "--tn-yellow"],
  ["warning", "--tn-orange"],
  ["danger", "--tn-red"],
  ["danger", "--tn-magenta"],
  ["accent", "--tn-purple"],
];

const ALL_TN_PROPS = [...new Set(THEME_TO_TN.map(([, prop]) => prop))];

function applyPlaygroundTheme(preset: FwThemePreset): void {
  const root = document.documentElement;

  if (preset === "default") {
    for (const prop of ALL_TN_PROPS) root.style.removeProperty(prop);
    return;
  }

  const overrides = resolveTheme(preset) ?? CSS_ONLY_OVERRIDES[preset];
  if (!overrides) return;

  for (const [key, prop] of THEME_TO_TN) {
    const value = overrides[key];
    if (value) root.style.setProperty(prop, value);
  }
}

function getInitialValue(key: string, fallback: string): string {
  if (typeof window === "undefined") return fallback;
  return loadString(key) ?? fallback;
}

export function PlaygroundProvider({ children }: { children: ReactNode }) {
  // Connection config (persisted)
  const [baseUrl, setBaseUrlState] = useState(() =>
    getInitialValue(STORAGE_KEYS.baseUrl, DEFAULTS.baseUrl)
  );
  const [viewerPath, setViewerPathState] = useState(() =>
    getInitialValue(STORAGE_KEYS.viewerPath, DEFAULTS.viewerPath)
  );
  const [streamName, setStreamNameState] = useState(() =>
    getInitialValue(STORAGE_KEYS.streamName, DEFAULTS.streamName)
  );
  const [thumbnailUrl, setThumbnailUrlState] = useState(() =>
    getInitialValue(STORAGE_KEYS.thumbnailUrl, DEFAULTS.thumbnailUrl)
  );

  // Player config
  const [autoplayMuted, setAutoplayMuted] = useState(DEFAULTS.autoplayMuted);
  const [selectedProtocol, setSelectedProtocol] = useState<string | null>(null);
  const [selectedPlayer, setSelectedPlayer] = useState<PlayerType>("auto");
  const [theme, setTheme] =
    useState<import("@livepeer-frameworks/player-core").FwThemePreset>("default");

  // Polled playback state
  const [playbackSources, setPlaybackSources] = useState<MistSource[]>([]);
  const [playbackLoading, setPlaybackLoading] = useState(false);
  const [playbackError, setPlaybackError] = useState<string | null>(null);

  // Active streams (auto-polled)
  const [activeStreams, setActiveStreams] = useState<string[]>([]);
  const [connectionStatus, setConnectionStatus] = useState<ConnectionStatus>("idle");

  // Derived values
  const viewerBase = useMemo(() => buildViewerBase(baseUrl, viewerPath), [baseUrl, viewerPath]);
  const host = useMemo(() => extractHostname(baseUrl), [baseUrl]);
  const ingestUris = useMemo<IngestUris>(
    () => buildIngestUris(baseUrl, streamName),
    [baseUrl, streamName]
  );

  // Persist config changes
  useEffect(() => {
    saveString(STORAGE_KEYS.baseUrl, baseUrl);
  }, [baseUrl]);

  useEffect(() => {
    saveString(STORAGE_KEYS.viewerPath, viewerPath);
  }, [viewerPath]);

  useEffect(() => {
    saveString(STORAGE_KEYS.streamName, streamName);
  }, [streamName]);

  useEffect(() => {
    saveString(STORAGE_KEYS.thumbnailUrl, thumbnailUrl);
  }, [thumbnailUrl]);

  // Fetch playback sources from MistServer
  const fetchPlaybackSources = useCallback(async () => {
    if (!streamName.trim()) {
      setPlaybackSources([]);
      setPlaybackError(null);
      return;
    }

    const url = buildMistJsonUrl(viewerBase, streamName);
    setPlaybackLoading(true);

    try {
      const response = await fetch(url, { cache: "no-store" });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }

      const data = await response.json();

      if (data.error) {
        setPlaybackError(data.error);
        setPlaybackSources([]);
      } else if (Array.isArray(data.source)) {
        setPlaybackSources(data.source);
        setPlaybackError(null);
      } else {
        setPlaybackSources([]);
        setPlaybackError(null);
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to fetch";
      setPlaybackError(message);
      setPlaybackSources([]);
    } finally {
      setPlaybackLoading(false);
    }
  }, [viewerBase, streamName]);

  // Actions
  const setBaseUrl = useCallback((url: string) => {
    setBaseUrlState(sanitizeBaseUrl(url));
  }, []);

  const setViewerPath = useCallback((path: string) => {
    setViewerPathState(path);
  }, []);

  const setStreamName = useCallback((name: string) => {
    setStreamNameState(name);
  }, []);

  const setThumbnailUrl = useCallback((url: string) => {
    setThumbnailUrlState(url);
  }, []);

  const pollSources = useCallback(() => {
    fetchPlaybackSources();
  }, [fetchPlaybackSources]);

  const refreshStreams = useCallback(() => {
    fetchPlaybackSources();
  }, [fetchPlaybackSources]);

  // Apply theme to playground shell (updates :root --tn-* variables)
  useEffect(() => {
    applyPlaygroundTheme(theme);
  }, [theme]);

  // Derive connection status + active streams from playback sources
  // (polled via /json_{streamName}.js — the correct MistServer HTTP output endpoint)
  useEffect(() => {
    if (playbackLoading) return;
    if (playbackSources.length > 0) {
      setConnectionStatus("connected");
      setActiveStreams([streamName]);
    } else if (playbackError) {
      setConnectionStatus("failed");
      setActiveStreams([]);
    } else {
      setConnectionStatus("idle");
      setActiveStreams([]);
    }
  }, [playbackSources, playbackError, playbackLoading, streamName]);

  // Auto-poll stream info via /json_{streamName}.js
  useEffect(() => {
    fetchPlaybackSources();
    const interval = setInterval(fetchPlaybackSources, POLL_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [fetchPlaybackSources]);

  const value = useMemo<PlaygroundContextValue>(
    () => ({
      // State
      baseUrl,
      viewerPath,
      streamName,
      viewerBase,
      host,
      ingestUris,
      playbackSources,
      playbackLoading,
      playbackError,
      activeStreams,
      connectionStatus,
      thumbnailUrl,
      autoplayMuted,
      selectedProtocol,
      selectedPlayer,
      theme,
      // Actions
      setBaseUrl,
      setViewerPath,
      setStreamName,
      setThumbnailUrl,
      setAutoplayMuted,
      pollSources,
      refreshStreams,
      setSelectedProtocol,
      setSelectedPlayer,
      setTheme,
    }),
    [
      baseUrl,
      viewerPath,
      streamName,
      viewerBase,
      host,
      ingestUris,
      playbackSources,
      playbackLoading,
      playbackError,
      activeStreams,
      connectionStatus,
      thumbnailUrl,
      autoplayMuted,
      selectedProtocol,
      selectedPlayer,
      setBaseUrl,
      setViewerPath,
      setStreamName,
      setThumbnailUrl,
      setAutoplayMuted,
      pollSources,
      refreshStreams,
      setSelectedProtocol,
      setSelectedPlayer,
      theme,
      setTheme,
    ]
  );

  return <PlaygroundContext.Provider value={value}>{children}</PlaygroundContext.Provider>;
}
