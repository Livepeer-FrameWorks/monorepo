import React, { useState, useEffect, useRef, useMemo } from "react";
import LoadingScreen from "./LoadingScreen";
import ThumbnailOverlay from "./ThumbnailOverlay";
import PlayerControls from "./PlayerControls";
import StreamStateOverlay from "./StreamStateOverlay";
import { PlayerProvider, usePlayer } from "../context/PlayerContext";
import { PlayerProps, EndpointInfo, OutputEndpoint, OutputCapabilities, PlayerState, PlayerStateContext } from "../types";
import useViewerEndpoints from "../hooks/useViewerEndpoints";
import { useStreamState } from "../hooks/useStreamState";
import { globalPlayerManager, StreamInfo, StreamSource, StreamTrack } from "../core";

/**
 * Inner player component that uses PlayerContext
 */
const PlayerInner: React.FC<PlayerProps> = ({
  contentId,
  contentType,
  thumbnailUrl = null,
  options,
  endpoints,
  onStateChange
}) => {
  const { setPlayer } = usePlayer();
  const [isPlaying, setIsPlaying] = useState<boolean>(false);
  const [isMuted, setIsMuted] = useState<boolean>(true);
  const [currentTime, setCurrentTime] = useState<number>(0);
  const [duration, setDuration] = useState<number>(NaN);
  const [isBuffering, setIsBuffering] = useState<boolean>(false);
  const [errorText, setErrorText] = useState<string | null>(null);
  const lastStateRef = useRef<PlayerState | null>(null);
  const supportsOverlay = false;
  const containerRef = useRef<HTMLDivElement | null>(null);

  const handlePlay = () => {
    setIsPlaying(true);
    setIsMuted(false);
  };

  // Show loading state while contacting load balancer
  // If endpoints not passed in, fetch via Gateway
  const gw = options?.gatewayUrl;
  const { endpoints: fetchedEndpoints, status: fetchStatus } = useViewerEndpoints(
    gw ? { gatewayUrl: gw, contentType, contentId, authToken: options?.authToken } : ({} as any)
  );
  const ep = endpoints?.primary ? endpoints : fetchedEndpoints || undefined;

  // Extract MistServer base URL for native communication
  const mistBaseUrl = ep?.primary?.baseUrl || '';

  // Stream state polling via native MistServer WebSocket/HTTP
  const {
    status: streamStatus,
    isOnline: streamIsOnline,
    message: streamMessage,
    percentage: streamPercentage,
    refetch: refetchStreamState,
  } = useStreamState({
    mistBaseUrl,
    streamName: contentId,
    enabled: Boolean(mistBaseUrl) && contentType === 'live',
    useWebSocket: true,
    pollInterval: 3000,
  });

  // Emit helper for state
  const emit = (state: PlayerState, context?: PlayerStateContext) => {
    if (lastStateRef.current !== state) {
      lastStateRef.current = state;
      try { onStateChange?.(state, context); } catch {}
    }
  };

  // Initial booting state
  useEffect(() => { emit('booting'); /* one-time */ // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Gateway status mapping
  useEffect(() => {
    if (!gw) return;
    if (fetchStatus === 'loading') emit('gateway_loading', { gatewayStatus: fetchStatus });
    else if (fetchStatus === 'ready') emit('gateway_ready', { gatewayStatus: fetchStatus });
    else if (fetchStatus === 'error') emit('gateway_error', { gatewayStatus: fetchStatus });
  }, [fetchStatus, gw]);

  if (!ep?.primary) {
    emit('no_endpoint', { gatewayStatus: fetchStatus });
    const message = gw ? (fetchStatus === 'loading' ? 'Resolving viewing endpoint...' : 'Waiting for endpoint...') : 'Waiting for endpoint...';
    return <LoadingScreen message={message} />;
  }

  const primary: EndpointInfo | undefined = ep?.primary as EndpointInfo | undefined;

  // Build StreamInfo for PlayerManager from backend-provided outputs only
  const sources: StreamSource[] = [];
  const outputs = (primary?.outputs || {}) as Record<string, OutputEndpoint>;
  const oKeys = Object.keys(outputs);

  const attachMistSource = (html?: string, playerJs?: string) => {
    if (!html && !playerJs) return;
    const src: any = { url: html || playerJs || '', type: 'mist/html', streamName: contentId };
    if (playerJs) { src.mistPlayerUrl = playerJs; }
    sources.push(src);
  };

  if (oKeys.length) {
    const html = outputs['MIST_HTML']?.url;
    const pjs = outputs['PLAYER_JS']?.url;
    attachMistSource(html, pjs);
    if (outputs['WHEP']?.url) {
      sources.push({ url: outputs['WHEP'].url, type: 'whep' });
    }
    if (outputs['MP4']?.url) {
      sources.push({ url: outputs['MP4'].url, type: 'html5/video/mp4' });
    }
    if (outputs['WEBM']?.url) {
      sources.push({ url: outputs['WEBM'].url, type: 'html5/video/webm' });
    }
    if (outputs['MEWS_WS']?.url) {
      sources.push({ url: outputs['MEWS_WS'].url, type: 'ws/video/mp4' });
    }
    // Optional explicit HLS/DASH if provided
    if (outputs['HLS']?.url) {
      sources.push({ url: outputs['HLS'].url, type: 'html5/application/vnd.apple.mpegurl' });
    }
    if (outputs['DASH']?.url) {
      sources.push({ url: outputs['DASH'].url, type: 'dash/video/mp4' });
    }
  } else if (primary) {
    // Fallback: single primary
    sources.push({ url: primary.url, type: primary.protocol || 'mist/html', streamName: contentId } as any);
  }

  // Derive minimal track meta from capabilities, if available
  const tracks: StreamTrack[] = [];
  const pushCodecTracks = (cap?: OutputCapabilities) => {
    if (!cap) return;
    const codecs = cap.codecs || [];
    const addTrack = (type: 'video' | 'audio', codecstr: string) => {
      tracks.push({ type, codec: mapCodecLabel(codecstr), codecstring: codecstr });
    };
    codecs.forEach((c) => {
      const lc = c.toLowerCase();
      if (lc.startsWith('avc1') || lc.startsWith('hev1') || lc.startsWith('hvc1') || lc.startsWith('vp') || lc.startsWith('av01')) {
        addTrack('video', c);
      } else if (lc.startsWith('mp4a') || lc.includes('opus') || lc.includes('vorbis') || lc.includes('ac3') || lc.includes('ec-3')) {
        addTrack('audio', c);
      }
    });
    if (!codecs.length) {
      if (cap.hasVideo) tracks.push({ type: 'video', codec: 'H264' });
      if (cap.hasAudio) tracks.push({ type: 'audio', codec: 'AAC' });
    }
  };
  Object.values(outputs).forEach((out) => pushCodecTracks(out.capabilities));
  if (!tracks.length) {
    // Ensure at least a generic video track
    tracks.push({ type: 'video', codec: 'H264' });
  }

  function mapCodecLabel(codecstr: string): string {
    const c = codecstr.toLowerCase();
    if (c.startsWith('avc1')) return 'H264';
    if (c.startsWith('hev1') || c.startsWith('hvc1')) return 'HEVC';
    if (c.startsWith('av01')) return 'AV1';
    if (c.startsWith('vp09')) return 'VP9';
    if (c.startsWith('vp8')) return 'VP8';
    if (c.startsWith('mp4a')) return 'AAC';
    if (c.includes('opus')) return 'Opus';
    if (c.includes('ec-3') || c.includes('ac3')) return 'AC3';
    return codecstr;
  }

  const streamInfo: StreamInfo | null = sources.length ? { source: sources, meta: { tracks } } : null;

  // Stable key for sources to avoid unnecessary re-renders
  // sources is constructed freshly each render, so we use JSON.stringify once in useMemo
  const sourcesKey = useMemo(
    () => sources.map(s => s.url).join('|'),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [primary?.nodeId, outputs]
  );

  // Initialize via PlayerManager
  useEffect(() => {
    const container = containerRef.current;
    if (!container || !streamInfo) return;
    // clear container
    container.innerHTML = '';
    // Listen for selection to report connecting
    const onSelected = (e: any) => emit('connecting', { selectedPlayer: e.player, selectedProtocol: (e.source?.type || '').toString(), endpointUrl: e.source?.url });
    try { (globalPlayerManager as any).on?.('playerSelected', onSelected); } catch {}
    globalPlayerManager
      .initializePlayer(container, streamInfo, {
        autoplay: options?.autoplay !== false,
        muted: options?.muted !== false,
        controls: options?.controls !== false,
        poster: thumbnailUrl || undefined,
        onReady: (el) => {
          // Set player in context for controls
          const currentPlayer = globalPlayerManager.getCurrentPlayer();
          setPlayer(currentPlayer);

          setDuration(isFinite(el.duration) ? el.duration : el.duration);
          const onDur = () => setDuration(isFinite(el.duration) ? el.duration : el.duration);
          el.addEventListener('durationchange', onDur);
          const onWaiting = () => { setIsBuffering(true); emit('buffering'); };
          const onPlaying = () => { setIsBuffering(false); emit('playing'); };
          const onCanPlay = () => { setIsBuffering(false); emit('playing'); };
          const onPause = () => emit('paused');
          const onEnded = () => emit('ended');
          const onErr = () => { setErrorText(el.error ? (el.error.message || 'Playback error') : 'Playback error'); emit('error', { error: el.error?.message || 'Playback error' }); };
          el.addEventListener('waiting', onWaiting);
          el.addEventListener('playing', onPlaying);
          el.addEventListener('canplay', onCanPlay);
          el.addEventListener('pause', onPause);
          el.addEventListener('ended', onEnded);
          el.addEventListener('error', onErr);
        },
        onTimeUpdate: (t) => {
          setCurrentTime(t);
        },
        onError: (err) => { setErrorText(typeof err === 'string' ? err : String(err)); emit('error', { error: typeof err === 'string' ? err : String(err) }); }
      })
      .catch((e) => console.warn('Player init failed', e));
    return () => {
      emit('destroyed');
      setPlayer(null); // Clear player from context on cleanup
      try { (globalPlayerManager as any).off?.('playerSelected', onSelected); } catch {}
      globalPlayerManager.destroy();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [primary?.nodeId, contentId, sourcesKey, setPlayer]);

  // If Gateway fetch failed, report offline
  useEffect(() => {
    if (!gw) return;
    if (fetchStatus === 'error') emit('gateway_error', { gatewayStatus: fetchStatus });
  }, [fetchStatus, gw]);

  // Determine what overlay to show
  let overlayComponent: React.ReactNode = null;
  
  // Click-to-play mode: show thumbnail overlay when not playing
  if (thumbnailUrl && supportsOverlay && !isPlaying) {
    overlayComponent = (
      <ThumbnailOverlay
        thumbnailUrl={thumbnailUrl}
        onPlay={handlePlay}
        message={contentId}
        showUnmuteMessage={false}
        className="fw-absolute inset-0 z-10"
      />
    );
  }
  // Autoplay muted mode: show simple overlay when muted
  else if (supportsOverlay && isMuted && isPlaying) {
    overlayComponent = (
      <ThumbnailOverlay
        thumbnailUrl={undefined}
        onPlay={handlePlay}
        message={null}
        showUnmuteMessage={true}
        className="fw-absolute inset-0 z-10"
      />
    );
  }

  // Always render player, conditionally render overlay on top
  const useStockControls = options?.stockControls === true;
  // Keyboard: hold space to increase speed when not at live point; add F/M/Arrows shortcuts
  useEffect(() => {
    if (useStockControls) return;
    const onKey = (e: KeyboardEvent) => {
      const p = globalPlayerManager.getCurrentPlayer();
      const v = p?.getVideoElement();
      if (!p || !v) return;
      if (e.code === 'Space') {
        e.preventDefault();
        const nearLive = isFinite(v.duration) ? (v.duration - v.currentTime < 2) : false;
        if (!nearLive) p.setPlaybackRate?.(1.5);
      } else if (e.code === 'KeyF') {
        e.preventDefault();
        const el = containerRef.current;
        if (!el) return;
        if (document.fullscreenElement) { document.exitFullscreen().catch(() => {}); }
        else { el.requestFullscreen().catch(() => {}); }
      } else if (e.code === 'KeyM') {
        e.preventDefault();
        p.setMuted?.(!p.isMuted?.());
      } else if (e.code === 'ArrowLeft') {
        e.preventDefault();
        v.currentTime = Math.max(0, v.currentTime - 5);
      } else if (e.code === 'ArrowRight') {
        e.preventDefault();
        const cap = isFinite(v.duration) ? v.duration : v.currentTime + 5;
        v.currentTime = Math.min(cap, v.currentTime + 5);
      } else if (e.code === 'ArrowUp') {
        e.preventDefault();
        v.volume = Math.max(0, Math.min(1, v.volume + 0.05));
      } else if (e.code === 'ArrowDown') {
        e.preventDefault();
        v.volume = Math.max(0, Math.min(1, v.volume - 0.05));
      }
    };
    const onKeyUp = (e: KeyboardEvent) => {
      if (e.code === 'Space') {
        const p = globalPlayerManager.getCurrentPlayer();
        p?.setPlaybackRate?.(1);
      }
    };
    window.addEventListener('keydown', onKey);
    window.addEventListener('keyup', onKeyUp);
    return () => {
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('keyup', onKeyUp);
    };
  }, [useStockControls]);

  // For live streams, show stream state overlay if not online
  const showStreamStateOverlay = contentType === 'live' && mistBaseUrl && !streamIsOnline && streamStatus !== 'ONLINE';

  return (
    <div className="fw-player-root" data-player-container="true">
      <div ref={containerRef} className="fw-player-container" />

      {/* Stream state overlay for live streams (offline/initializing/etc.) */}
      {showStreamStateOverlay && (
        <StreamStateOverlay
          status={streamStatus}
          message={streamMessage}
          percentage={streamPercentage}
          onRetry={refetchStreamState}
          visible={true}
        />
      )}

      {/* Buffering/Error overlay */}
      {!showStreamStateOverlay && (isBuffering || errorText) && (
        <div
          role="status"
          aria-live="polite"
          className="fw-fw-player-surface absolute inset-0 flex items-center justify-center bg-black/40 backdrop-blur-sm"
        >
          <div className="fw-flex items-center gap-3 rounded-lg border border-white/10 bg-black/70 px-4 py-3 text-sm text-white shadow-lg">
            <span>{errorText ? "Playback error" : "Buffering…"}</span>
            {errorText && (
              <button
                type="button"
                className="fw-rounded-md bg-white/90 px-3 py-1 text-xs font-medium text-black transition hover:bg-white"
                aria-label="Retry playback"
                onClick={() => {
                  setErrorText(null);
                  setIsBuffering(false);
                  const container = containerRef.current;
                  if (!container || !streamInfo) return;
                  try {
                    globalPlayerManager.destroy();
                  } catch {
                    /* noop */
                  }
                  container.innerHTML = "";
                  globalPlayerManager
                    .initializePlayer(container, streamInfo, {
                      autoplay: options?.autoplay !== false,
                      muted: options?.muted !== false,
                      controls: options?.controls !== false,
                      poster: thumbnailUrl || undefined,
                      onTimeUpdate: (t) => setCurrentTime(t),
                      onError: (err) => setErrorText(typeof err === "string" ? err : String(err))
                    })
                    .catch((e) => console.warn("Retry init failed", e));
                }}
              >
                Retry
              </button>
            )}
          </div>
        </div>
      )}
      {!useStockControls && overlayComponent}
      {/* New unified controls component */}
      {!useStockControls && (
        <PlayerControls
          currentTime={currentTime}
          duration={duration}
          isVisible={true}
          onSeek={setCurrentTime}
        />
      )}
    </div>
  );
};

/**
 * Main Player component wrapped with PlayerProvider.
 * This enables PlayerControls to access the player via context
 * instead of the global singleton.
 */
const Player: React.FC<PlayerProps> = (props) => {
  return (
    <PlayerProvider>
      <PlayerInner {...props} />
    </PlayerProvider>
  );
};

export default Player;
