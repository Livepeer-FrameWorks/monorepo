/**
 * MistServer Legacy Player - React Wrapper
 *
 * Fallback player using MistServer's native player.js.
 * The implementation is in @livepeer-frameworks/player-core.
 */

import React, { useEffect, useRef } from 'react';
import { MistPlayerImpl } from '@livepeer-frameworks/player-core';

// Re-export the implementation from core for backwards compatibility
export { MistPlayerImpl };

type Props = {
  src: string;
  streamName?: string;
  muted?: boolean;
  autoPlay?: boolean;
  controls?: boolean;
  devMode?: boolean;
  onError?: (e: Error) => void;
};

// React component wrapper
const MistPlayer: React.FC<Props> = ({
  src,
  streamName,
  muted = true,
  autoPlay = true,
  controls = true,
  devMode = false,
  onError
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const playerRef = useRef<MistPlayerImpl | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const player = new MistPlayerImpl();
    playerRef.current = player;

    player.initialize(
      containerRef.current,
      { url: src, type: 'mist/legacy', streamName },
      { autoplay: autoPlay, muted, controls, devMode }
    ).catch((e) => {
      onError?.(e instanceof Error ? e : new Error(String(e)));
    });

    return () => {
      player.destroy();
      playerRef.current = null;
    };
  }, [src, streamName, muted, autoPlay, controls, devMode, onError]);

  return <div ref={containerRef} style={{ width: '100%', height: '100%' }} />;
};

export default MistPlayer;
