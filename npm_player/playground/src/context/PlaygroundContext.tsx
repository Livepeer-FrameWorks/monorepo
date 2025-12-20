import { createContext, useContext, useState, useMemo, useEffect, useCallback, type ReactNode } from "react";
import type { PlaygroundState, PlaygroundActions, MistSource, IngestUris, PlayerType } from "../lib/types";
import { DEFAULTS, STORAGE_KEYS } from "../lib/constants";
import {
  sanitizeBaseUrl,
  buildViewerBase,
  buildIngestUris,
  buildMistJsonUrl,
  loadString,
  saveString,
  extractHostname
} from "../lib/mist-utils";

type PlaygroundContextValue = PlaygroundState & PlaygroundActions;

const PlaygroundContext = createContext<PlaygroundContextValue | null>(null);

function getInitialValue(key: string, fallback: string): string {
  if (typeof window === "undefined") return fallback;
  return loadString(key) ?? fallback;
}

export function PlaygroundProvider({ children }: { children: ReactNode }) {
  // Connection config (persisted)
  const [baseUrl, setBaseUrlState] = useState(() => getInitialValue(STORAGE_KEYS.baseUrl, DEFAULTS.baseUrl));
  const [viewerPath, setViewerPathState] = useState(() => getInitialValue(STORAGE_KEYS.viewerPath, DEFAULTS.viewerPath));
  const [streamName, setStreamNameState] = useState(() => getInitialValue(STORAGE_KEYS.streamName, DEFAULTS.streamName));
  const [thumbnailUrl, setThumbnailUrlState] = useState(() => getInitialValue(STORAGE_KEYS.thumbnailUrl, DEFAULTS.thumbnailUrl));

  // Player config
  const [autoplayMuted, setAutoplayMuted] = useState(DEFAULTS.autoplayMuted);
  const [selectedProtocol, setSelectedProtocol] = useState<string | null>(null);
  const [selectedPlayer, setSelectedPlayer] = useState<PlayerType>("auto");

  // Polled playback state
  const [playbackSources, setPlaybackSources] = useState<MistSource[]>([]);
  const [playbackLoading, setPlaybackLoading] = useState(false);
  const [playbackError, setPlaybackError] = useState<string | null>(null);

  // Derived values
  const viewerBase = useMemo(() => buildViewerBase(baseUrl, viewerPath), [baseUrl, viewerPath]);
  const host = useMemo(() => extractHostname(baseUrl), [baseUrl]);
  const ingestUris = useMemo<IngestUris>(() => buildIngestUris(baseUrl, streamName), [baseUrl, streamName]);

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
      thumbnailUrl,
      autoplayMuted,
      selectedProtocol,
      selectedPlayer,
      // Actions
      setBaseUrl,
      setViewerPath,
      setStreamName,
      setThumbnailUrl,
      setAutoplayMuted,
      pollSources,
      setSelectedProtocol,
      setSelectedPlayer
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
      setSelectedProtocol,
      setSelectedPlayer
    ]
  );

  return <PlaygroundContext.Provider value={value}>{children}</PlaygroundContext.Provider>;
}

export function usePlayground(): PlaygroundContextValue {
  const context = useContext(PlaygroundContext);
  if (!context) {
    throw new Error("usePlayground must be used within a PlaygroundProvider");
  }
  return context;
}
