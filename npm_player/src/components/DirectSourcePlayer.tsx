import React, { useRef, useEffect } from 'react';
import { DirectSourcePlayerProps } from '../types';

/**
 * Direct source player for MP4/HLS/DASH.
 */
const DirectSourcePlayer: React.FC<DirectSourcePlayerProps> = ({ src, muted = true, controls = true, poster, onError }) => {
  const videoRef = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const el = videoRef.current;
    if (!el) return;
    el.muted = muted;
  }, [muted]);

  // Keep video element in sync when props change
  useEffect(() => {
    const el = videoRef.current;
    if (!el) return;
    el.muted = muted;
  }, [muted]);

  return (
    <video
      style={{ 
        width: '100%', 
        maxWidth: '100%', 
        height: '100%' 
      }}
      autoPlay
      muted={muted}
      controls={controls}
      playsInline
      ref={videoRef}
      src={src}
      poster={poster}
      onError={onError}
    />
  );
};

export default DirectSourcePlayer; 