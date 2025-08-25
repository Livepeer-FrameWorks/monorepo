import React, { useEffect, useRef } from 'react';
import { MistPlayerProps } from '../types';

// Extend Window interface to include MistPlayer globals
declare global {
  interface Window {
    mistplayers?: any;
    mistPlay?: (streamName: string, options: any) => void;
  }
}

/**
 * MistPlayer Component
 * 
 * A React component that embeds the MistServer player for streaming video content.
 * Accepts pre-resolved URIs from the parent Player component.
 */
const MistPlayer: React.FC<MistPlayerProps> = ({
  streamName,
  htmlUrl,
  playerJsUrl,
  developmentMode = false,
  muted = true,
  poster,
}) => {
  const isBrowser = typeof window !== 'undefined';
  const containerRef = useRef<HTMLDivElement | null>(null);
  // Use iframe when explicit HTML embed URL is provided
  const useIFrame = isBrowser && !!htmlUrl;
  const htmlUri = (htmlUrl || '') + (htmlUrl && developmentMode ? (htmlUrl.includes('?') ? '&dev=1' : '?dev=1') : '');
  const playerUri = playerJsUrl || '';

  useEffect(() => {
    if (!isBrowser) return;
    if (useIFrame || !playerUri) {
      return;
    }

    function playStream() {
      if (!window.mistplayers) return;

      // Try to start it on the non-transcoded track and using WebRTC
      window.mistPlay?.(streamName, {
        target: containerRef.current || document.getElementById('mistplayer'),
        poster: poster || '/android-chrome-512x512.png',
        autoplay: true,
        ABR_resize: false,
        forcePriority: {
          source: [
            [
              'type',
              [
                'webrtc',
                'whep',
                'html5/video/webm',
                'ws/video/mp4',
                'html5/application/vnd.apple.mpegurl',
              ],
            ],
          ],
        },
        loop: false,
        controls: true,
        fillSpace: true,
        muted: muted,
        skin: developmentMode ? 'dev' : 'default',
      });
    }

    // Load meta player code from provided player.js URL
    if (!window.mistplayers) {
      const script = document.createElement('script');
      console.log('Loading new MistServer player from', playerUri);
      script.src = playerUri;
      document.head.appendChild(script);
      script.onload = playStream;
    } else {
      playStream();
    }
    return () => {
      try {
        // Attempt unload if Mist created a reference on the container
        const el = containerRef.current || document.getElementById('mistplayer');
        // @ts-ignore
        const ref = el && (el as any).MistVideoObject?.reference;
        if (ref && typeof ref.unload === 'function') {
          ref.unload();
        }
      } catch {}
    };
  }, [isBrowser, streamName, playerUri, useIFrame, developmentMode, muted, poster]);

  if (useIFrame) {
    const iframeElement = (
      <iframe
        style={{
          width: '100%',
          maxWidth: '100%',
          height: '100%',
          border: 'none',
          minHeight: '300px',
        }}
        src={htmlUri}
        title={`MistPlayer - ${streamName}`}
      />
    );
    return iframeElement;
  }

  return <div
    ref={containerRef}
    id="mistplayer"
    style={{
      width: '100%',
      maxWidth: '100%',
      height: '100%',
    }}
  >
    <noscript>
      <a href={htmlUri} target="_blank" rel="noopener noreferrer">
        Click here to play this video
      </a>
    </noscript>
  </div>;
};

export default MistPlayer; 