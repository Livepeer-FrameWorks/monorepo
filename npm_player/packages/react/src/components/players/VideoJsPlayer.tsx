/**
 * Video.js Player - React Wrapper
 *
 * HLS streaming via Video.js with VHS (videojs-http-streaming).
 * The implementation is in @livepeer-frameworks/player-core.
 */

import React, { useEffect, useRef } from 'react';
import { VideoJsPlayerImpl } from '@livepeer-frameworks/player-core';

// Re-export the implementation from core for backwards compatibility
export { VideoJsPlayerImpl };

type Props = {
  src: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  onError?: (e: Error) => void;
};

// React component wrapper
const VideoJsPlayer: React.FC<Props> = ({
  src,
  muted = true,
  autoPlay = true,
  controls = true,
  onError
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const playerRef = useRef<VideoJsPlayerImpl | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const player = new VideoJsPlayerImpl();
    playerRef.current = player;

    player.initialize(
      containerRef.current,
      { url: src, type: 'html5/application/vnd.apple.mpegurl' },
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

export default VideoJsPlayer;
