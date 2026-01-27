/**
 * MEWS WebSocket Player - React Wrapper
 *
 * Low-latency WebSocket MP4 streaming using MediaSource Extensions.
 * The implementation is in @livepeer-frameworks/player-core.
 */

import React, { useEffect, useRef } from "react";
import { MewsWsPlayerImpl } from "@livepeer-frameworks/player-core";

// Re-export the implementation from core for backwards compatibility
export { MewsWsPlayerImpl };

type Props = {
  wsUrl: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  onError?: (e: Error) => void;
};

// React component wrapper
const MewsWsPlayer: React.FC<Props> = ({
  wsUrl,
  muted = true,
  autoPlay = true,
  controls = true,
  onError,
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const playerRef = useRef<MewsWsPlayerImpl | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const player = new MewsWsPlayerImpl();
    playerRef.current = player;

    player
      .initialize(
        containerRef.current,
        { url: wsUrl, type: "ws/video/mp4" },
        { autoplay: autoPlay, muted, controls }
      )
      .catch((e) => {
        onError?.(e instanceof Error ? e : new Error(String(e)));
      });

    return () => {
      player.destroy();
      playerRef.current = null;
    };
  }, [wsUrl, muted, autoPlay, controls, onError]);

  return <div ref={containerRef} style={{ width: "100%", height: "100%" }} />;
};

export default MewsWsPlayer;
