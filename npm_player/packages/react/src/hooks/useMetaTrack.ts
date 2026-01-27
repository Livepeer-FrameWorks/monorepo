import { useEffect, useState, useRef, useCallback } from "react";
import { MetaTrackManager, type MetaTrackEvent } from "@livepeer-frameworks/player-core";
import type { UseMetaTrackOptions } from "../types";

export interface UseMetaTrackReturn {
  /** Whether connected to MistServer WebSocket */
  isConnected: boolean;
  /** Connection state */
  connectionState: "disconnected" | "connecting" | "connected" | "reconnecting";
  /** List of subscribed track IDs */
  subscribedTracks: string[];
  /** Subscribe to a meta track */
  subscribe: (trackId: string, callback: (event: MetaTrackEvent) => void) => () => void;
  /** Unsubscribe from a meta track */
  unsubscribe: (trackId: string, callback: (event: MetaTrackEvent) => void) => void;
  /** Manually connect */
  connect: () => void;
  /** Manually disconnect */
  disconnect: () => void;
  /** Update playback time for timed event dispatch (call on video timeupdate) */
  setPlaybackTime: (timeInSeconds: number) => void;
  /** Handle seek event (call on video seeking/seeked) */
  onSeek: (newTimeInSeconds: number) => void;
}

/**
 * Hook for subscribing to real-time metadata from MistServer
 *
 * Uses native MistServer WebSocket protocol for low-latency metadata delivery:
 * - Subtitles/captions
 * - Live scores
 * - Timed events
 * - Chapter markers
 *
 * @example
 * ```tsx
 * const { isConnected, subscribe } = useMetaTrack({
 *   mistBaseUrl: 'https://mist.example.com',
 *   streamName: 'pk_...', // playbackId (view key)
 *   enabled: true,
 * });
 *
 * useEffect(() => {
 *   if (!isConnected) return;
 *
 *   const unsubscribe = subscribe('1', (event) => {
 *     if (event.type === 'subtitle') {
 *       setSubtitle(event.data as SubtitleCue);
 *     }
 *   });
 *
 *   return unsubscribe;
 * }, [isConnected, subscribe]);
 * ```
 */
export function useMetaTrack(options: UseMetaTrackOptions): UseMetaTrackReturn {
  const { mistBaseUrl, streamName, subscriptions: initialSubscriptions, enabled = true } = options;

  const [isConnected, setIsConnected] = useState(false);
  const [connectionState, setConnectionState] = useState<
    "disconnected" | "connecting" | "connected" | "reconnecting"
  >("disconnected");
  const [subscribedTracks, setSubscribedTracks] = useState<string[]>([]);
  const managerRef = useRef<MetaTrackManager | null>(null);

  // Create manager instance
  useEffect(() => {
    if (!enabled || !mistBaseUrl || !streamName) {
      if (managerRef.current) {
        managerRef.current.disconnect();
        managerRef.current = null;
      }
      setIsConnected(false);
      setConnectionState("disconnected");
      return;
    }

    managerRef.current = new MetaTrackManager({
      mistBaseUrl,
      streamName,
      subscriptions: initialSubscriptions,
      debug: false,
    });

    // Start polling connection state
    const pollState = () => {
      if (managerRef.current) {
        const state = managerRef.current.getState();
        setConnectionState(state);
        setIsConnected(state === "connected");
        setSubscribedTracks(managerRef.current.getSubscribedTracks());
      }
    };

    const pollInterval = setInterval(pollState, 500);

    // Connect
    managerRef.current.connect();
    pollState();

    return () => {
      clearInterval(pollInterval);
      if (managerRef.current) {
        managerRef.current.disconnect();
        managerRef.current = null;
      }
      setIsConnected(false);
      setConnectionState("disconnected");
    };
  }, [enabled, mistBaseUrl, streamName, initialSubscriptions]);

  /**
   * Subscribe to a meta track
   */
  const subscribe = useCallback(
    (trackId: string, callback: (event: MetaTrackEvent) => void): (() => void) => {
      if (!managerRef.current) {
        return () => {};
      }

      const unsubscribe = managerRef.current.subscribe(trackId, callback);
      setSubscribedTracks(managerRef.current.getSubscribedTracks());

      return () => {
        unsubscribe();
        if (managerRef.current) {
          setSubscribedTracks(managerRef.current.getSubscribedTracks());
        }
      };
    },
    []
  );

  /**
   * Unsubscribe from a meta track
   */
  const unsubscribe = useCallback((trackId: string, callback: (event: MetaTrackEvent) => void) => {
    if (managerRef.current) {
      managerRef.current.unsubscribe(trackId, callback);
      setSubscribedTracks(managerRef.current.getSubscribedTracks());
    }
  }, []);

  /**
   * Manually connect
   */
  const connect = useCallback(() => {
    managerRef.current?.connect();
  }, []);

  /**
   * Manually disconnect
   */
  const disconnect = useCallback(() => {
    managerRef.current?.disconnect();
  }, []);

  /**
   * Update playback time for timed event dispatch
   * Call this on video timeupdate events to keep subtitle/chapter timing in sync
   */
  const setPlaybackTime = useCallback((timeInSeconds: number) => {
    managerRef.current?.setPlaybackTime(timeInSeconds);
  }, []);

  /**
   * Handle seek event - clears buffered events and resets state
   * Call this on video seeking/seeked events
   */
  const onSeek = useCallback((newTimeInSeconds: number) => {
    managerRef.current?.onSeek(newTimeInSeconds);
  }, []);

  return {
    isConnected,
    connectionState,
    subscribedTracks,
    subscribe,
    unsubscribe,
    connect,
    disconnect,
    setPlaybackTime,
    onSeek,
  };
}

export default useMetaTrack;
