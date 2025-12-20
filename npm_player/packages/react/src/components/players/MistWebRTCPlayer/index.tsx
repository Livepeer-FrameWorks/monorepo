/**
 * MistWebRTC Player - React Wrapper
 *
 * MistServer native WebRTC with signaling for DVR support.
 * The implementation is in @livepeer-frameworks/player-core.
 */

import React, { useRef, useEffect } from 'react';
import { MistWebRTCPlayerImpl } from '@livepeer-frameworks/player-core';

// Re-export the implementation from core for backwards compatibility
export { MistWebRTCPlayerImpl };

interface Props {
  src: string;
  autoPlay?: boolean;
  muted?: boolean;
  controls?: boolean;
  poster?: string;
  onReady?: (video: HTMLVideoElement) => void;
  onError?: (error: Error) => void;
}

export const MistWebRTCPlayer: React.FC<Props> = ({
  src,
  autoPlay = true,
  muted = true,
  controls = true,
  poster,
  onReady,
  onError,
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const playerRef = useRef<MistWebRTCPlayerImpl | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const player = new MistWebRTCPlayerImpl();
    playerRef.current = player;

    player.initialize(
      containerRef.current,
      { url: src, type: 'webrtc' },
      { autoplay: autoPlay, muted, controls, poster, onReady, onError: (e) => onError?.(typeof e === 'string' ? new Error(e) : e) }
    ).catch((e) => {
      onError?.(e instanceof Error ? e : new Error(String(e)));
    });

    return () => {
      player.destroy();
      playerRef.current = null;
    };
  }, [src, autoPlay, muted, controls, poster, onReady, onError]);

  return <div ref={containerRef} style={{ width: '100%', height: '100%' }} />;
};

export default MistWebRTCPlayerImpl;
