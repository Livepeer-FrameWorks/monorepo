import React, { useEffect, useRef } from 'react';
import { MistPlayerProps } from '../../types';
import { BasePlayer, StreamSource, StreamInfo, PlayerOptions, PlayerCapability } from '../../core/PlayerInterface';

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
  // Use iframe when explicit HTML embed URL is provided or when protocol mismatch detected
  const useIFrame = isBrowser && !!htmlUrl && (() => {
    try {
      const pageProto = window.location.protocol;
      const htmlProto = new URL(htmlUrl!, window.location.href).protocol;
      const playerProto = playerJsUrl ? new URL(playerJsUrl, window.location.href).protocol : null;
      if (playerProto && pageProto !== playerProto) return true;
      return false;
    } catch { return !!htmlUrl; }
  })();
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
        const el = containerRef.current || document.getElementById('mistplayer');
        // @ts-ignore
        const ref = el && (el as any).MistVideoObject?.reference;
        if (ref && typeof ref.unload === 'function') {
          ref.unload();
        } else if (el) {
          // Fallback: clear container to force player teardown
          (el as HTMLElement).innerHTML = '';
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

// Player implementation class  
export class MistPlayerImpl extends BasePlayer {
  readonly capability: PlayerCapability = {
    name: "MistServer Player",
    shortname: "mist",
    priority: 5,
    mimes: [
      "mist/html",
      "webrtc",
      "whep", 
      "html5/video/webm",
      "ws/video/mp4",
      "html5/application/vnd.apple.mpegurl"
    ]
  };

  private container: HTMLElement | null = null;
  private mistRef: any = null;

  isMimeSupported(mimetype: string): boolean {
    return this.capability.mimes.includes(mimetype);
  }

  isBrowserSupported(mimetype: string, source: StreamSource, streamInfo: StreamInfo): boolean | string[] {
    // MistPlayer is very versatile and supports most formats
    const playableTracks: string[] = [];
    
    for (const track of streamInfo.meta.tracks) {
      if (track.type === 'meta') {
        if (track.codec === 'subtitle') {
          playableTracks.push('subtitle');
        }
        continue;
      }
      playableTracks.push(track.type);
    }

    return playableTracks.length > 0 ? playableTracks : ['video', 'audio'];
  }

  async initialize(container: HTMLElement, source: StreamSource, options: PlayerOptions): Promise<HTMLVideoElement> {
    this.container = container;
    
    // Create a div for MistPlayer to use
    const mistDiv = document.createElement('div');
    mistDiv.style.width = '100%';
    mistDiv.style.height = '100%';
    container.appendChild(mistDiv);
    
    // Create a proxy video element for compatibility
    const video = document.createElement('video');
    video.style.display = 'none'; // Hidden since MistPlayer creates its own
    container.appendChild(video);
    
    this.videoElement = video;
    this.setupVideoEventListeners(video, options);

    try {
      // Load MistPlayer script dynamically 
      if (!(window as any).mistplayers && source.mistPlayerUrl) {
        await this.loadMistPlayer(source.mistPlayerUrl);
      }

      if ((window as any).mistPlay) {
        (window as any).mistPlay(source.streamName || 'unknown', {
          target: mistDiv,
          poster: options.poster,
          autoplay: options.autoplay,
          muted: options.muted,
          controls: options.controls,
          fillSpace: true,
          forcePriority: {
            source: [
              ['type', [
                'webrtc',
                'whep', 
                'html5/video/webm',
                'ws/video/mp4',
                'html5/application/vnd.apple.mpegurl'
              ]]
            ]
          }
        });
        
        // Store reference for cleanup
        this.mistRef = (mistDiv as any).MistVideoObject?.reference;
      }

      return video;
      
    } catch (error: any) {
      this.emit('error', error.message || 'MistPlayer initialization failed');
      throw error;
    }
  }

  private async loadMistPlayer(playerUrl: string): Promise<void> {
    return new Promise((resolve, reject) => {
      const script = document.createElement('script');
      script.src = playerUrl;
      script.onload = () => resolve();
      script.onerror = () => reject(new Error('Failed to load MistPlayer'));
      document.head.appendChild(script);
    });
  }

  destroy(): void {
    if (this.mistRef && typeof this.mistRef.unload === 'function') {
      try {
        this.mistRef.unload();
      } catch (e) {
        console.warn('Error unloading MistPlayer:', e);
      }
    }

    if (this.container) {
      // Clear the container completely
      this.container.innerHTML = '';
    }

    this.videoElement = null;
    this.container = null;
    this.mistRef = null;
    this.listeners.clear();
  }
}

export default MistPlayer; 