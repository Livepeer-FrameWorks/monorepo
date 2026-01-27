/**
 * Native Player - React Wrapper
 *
 * HTML5 video and WHEP WebRTC playback.
 * The implementation is in @livepeer-frameworks/player-core.
 */

import React, { useEffect, useRef } from "react";
import { NativePlayerImpl, DirectPlaybackPlayerImpl } from "@livepeer-frameworks/player-core";

// Re-export the implementations from core for backwards compatibility
export { NativePlayerImpl, DirectPlaybackPlayerImpl };

type Props = {
  src: string;
  type?: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  onError?: (e: Error) => void;
};

// React component wrapper
const NativePlayer: React.FC<Props> = ({
  src,
  type = "html5/video/mp4",
  muted = true,
  autoPlay = true,
  controls = true,
  onError,
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const playerRef = useRef<NativePlayerImpl | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const player = new NativePlayerImpl();
    playerRef.current = player;

    player
      .initialize(containerRef.current, { url: src, type }, { autoplay: autoPlay, muted, controls })
      .catch((e) => {
        onError?.(e instanceof Error ? e : new Error(String(e)));
      });

    return () => {
      player.destroy();
      playerRef.current = null;
    };
  }, [src, type, muted, autoPlay, controls, onError]);

  return <div ref={containerRef} style={{ width: "100%", height: "100%" }} />;
};

export default NativePlayer;
