/**
 * DASH.js Player - React Wrapper
 *
 * MPEG-DASH streaming via dash.js library.
 * The implementation is in @livepeer-frameworks/player-core.
 */

import React, { useEffect, useRef } from 'react';
import { DashJsPlayerImpl } from '@livepeer-frameworks/player-core';

// Re-export the implementation from core for backwards compatibility
export { DashJsPlayerImpl };

type Props = {
  src: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  onError?: (e: Error) => void;
};

// React component wrapper
const DashJsPlayer: React.FC<Props> = ({
  src,
  muted = true,
  autoPlay = true,
  controls = true,
  onError
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const playerRef = useRef<DashJsPlayerImpl | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const player = new DashJsPlayerImpl();
    playerRef.current = player;

    player.initialize(
      containerRef.current,
      { url: src, type: 'dash/video/mp4' },
      { autoplay: autoPlay, muted, controls }
    ).catch((e) => {
      onError?.(e instanceof Error ? e : new Error(String(e)));
    });

    return () => {
      player.destroy();
      playerRef.current = null;
    };
  }, [src, muted, autoPlay, controls, onError]);

  return <div ref={containerRef} style={{ width: '100%', height: '100%' }} />;
};

export default DashJsPlayer;
