import React, { useState, useEffect } from 'react';
import { Player } from './library';

function App() {
  // Get initial values from URL params or use defaults
  const getInitialStreamName = () => {
    const urlParams = new URLSearchParams(window.location.search);
    return urlParams.get('stream') || 'your-stream-name';
  };

  const getInitialPlayerType = () => {
    const urlParams = new URLSearchParams(window.location.search);
    const type = urlParams.get('playerType') || urlParams.get('player');
    return ['mist', 'canvas', 'whep'].includes(type) ? type : 'mist';
  };

  const getInitialThumbnailUrl = () => {
    const urlParams = new URLSearchParams(window.location.search);
    return urlParams.get('thumbnailUrl') || urlParams.get('thumbnail') || 'https://picsum.photos/800/450?random=1';
  };

  const getInitialThumbnailMode = () => {
    const urlParams = new URLSearchParams(window.location.search);
    const mode = urlParams.get('thumbnailMode') || urlParams.get('mode');

    // Support legacy parameters
    if (urlParams.get('autoplayMuted') === 'true') return 'autoplay-muted';
    if (urlParams.get('clickToPlay') === 'true') return 'click-to-play';

    return ['none', 'click-to-play', 'autoplay-muted'].includes(mode) ? mode : 'none';
  };

  const [streamName, setStreamName] = useState(getInitialStreamName());
  const [playerType, setPlayerType] = useState(getInitialPlayerType());
  const [thumbnailUrl, setThumbnailUrl] = useState(getInitialThumbnailUrl());
  const [thumbnailMode, setThumbnailMode] = useState(getInitialThumbnailMode());

  // Update URL when any parameter changes
  useEffect(() => {
    const url = new URL(window.location);

    // Stream name
    if (streamName && streamName !== 'your-stream-name') {
      url.searchParams.set('stream', streamName);
    } else {
      url.searchParams.delete('stream');
    }

    // Player type
    if (playerType !== 'mist') {
      url.searchParams.set('playerType', playerType);
    } else {
      url.searchParams.delete('playerType');
    }

    // Thumbnail URL
    if (thumbnailUrl !== 'https://picsum.photos/800/450?random=1') {
      url.searchParams.set('thumbnailUrl', thumbnailUrl);
    } else {
      url.searchParams.delete('thumbnailUrl');
    }

    // Thumbnail mode
    if (thumbnailMode !== 'none') {
      url.searchParams.set('thumbnailMode', thumbnailMode);
    } else {
      url.searchParams.delete('thumbnailMode');
    }

    window.history.replaceState({}, '', url);
  }, [streamName, playerType, thumbnailUrl, thumbnailMode]);

  const supportsThumbnails = true; // All player types now support thumbnails

  return (
    <div style={{ fontFamily: 'Arial, sans-serif', padding: '20px' }}>
      <header style={{ textAlign: 'center', marginBottom: '30px' }}>
        <h1>MistPlayer React Examples</h1>

        <div style={{ backgroundColor: '#f5f5f5', padding: '15px', borderRadius: '5px', marginBottom: '20px' }}>
          <h3>Stream Configuration</h3>
          <input
            type="text"
            placeholder="Stream Name"
            value={streamName}
            onChange={(e) => setStreamName(e.target.value)}
            style={{ padding: '10px', margin: '10px', borderRadius: '5px', border: '1px solid #ccc' }}
          />
          <p>Uses the load balancer to find the best streaming server automatically!</p>
          <p style={{ fontSize: '12px', color: '#666' }}>
            💡 Tip: Use URL parameters to configure the player:<br />
            • ?stream=your-stream-name (set stream)<br />
            • ?playerType=whep (mist/canvas/whep)<br />
            • ?thumbnailMode=autoplay-muted (none/click-to-play/autoplay-muted)<br />
            • ?thumbnailUrl=https://... (custom thumbnail)<br />
            Example: ?stream=test&playerType=whep&thumbnailMode=autoplay-muted
          </p>
        </div>

        <div style={{
          backgroundColor: supportsThumbnails ? '#e8f5e8' : '#f0f0f0',
          padding: '15px',
          borderRadius: '5px',
          marginBottom: '20px',
          opacity: supportsThumbnails ? 1 : 0.6
        }}>
          <h3>🖼️ Thumbnail Options</h3>
          <div style={{
            backgroundColor: '#e3f2fd',
            padding: '10px',
            borderRadius: '5px',
            marginBottom: '15px',
            border: '1px solid #90caf9'
          }}>
            <strong>ℹ️ Note:</strong> All player types support thumbnails! MistPlayer uses thumbnails as poster images,
            while Canvas and WHEP players support interactive overlays with click-to-play and autoplay-muted modes.
          </div>
          <div style={{ marginBottom: '15px' }}>
            <input
              type="text"
              placeholder="Thumbnail URL (optional)"
              value={thumbnailUrl}
              onChange={(e) => setThumbnailUrl(e.target.value)}
              style={{
                padding: '10px',
                margin: '5px',
                borderRadius: '5px',
                border: '1px solid #ccc',
                width: '300px'
              }}
            />
          </div>
          <div style={{ marginBottom: '10px' }}>
            <label style={{ marginRight: '20px' }}>
              <input
                type="radio"
                value="none"
                checked={thumbnailMode === 'none'}
                onChange={(e) => setThumbnailMode(e.target.value)}
                style={{ marginRight: '5px' }}
                disabled={!supportsThumbnails}
              />
              No Thumbnail
            </label>
            <label style={{ marginRight: '20px' }}>
              <input
                type="radio"
                value="click-to-play"
                checked={thumbnailMode === 'click-to-play'}
                onChange={(e) => setThumbnailMode(e.target.value)}
                style={{ marginRight: '5px' }}
                disabled={!supportsThumbnails}
              />
              Click to Play
            </label>
            <label>
              <input
                type="radio"
                value="autoplay-muted"
                checked={thumbnailMode === 'autoplay-muted'}
                onChange={(e) => setThumbnailMode(e.target.value)}
                style={{ marginRight: '5px' }}
                disabled={!supportsThumbnails}
              />
              Autoplay Muted
            </label>
          </div>
          <p style={{ fontSize: '12px', color: '#666', margin: '5px 0' }}>
            <strong>Click to Play:</strong> Shows thumbnail until user clicks to start stream<br />
            <strong>Autoplay Muted:</strong> Starts playing muted with "Click to unmute" overlay
          </p>
        </div>
      </header>

      <main style={{ maxWidth: '800px', margin: '0 auto' }}>
        <div>
          <h2>Player Component with Thumbnail Support</h2>
          <p>Clean player component with optional thumbnail functionality:</p>
          <div style={{ marginBottom: '20px' }}>
            <label style={{ marginRight: '20px' }}>
              <input
                type="radio"
                value="mist"
                checked={playerType === 'mist'}
                onChange={(e) => setPlayerType(e.target.value)}
                style={{ marginRight: '5px' }}
              />
              MistPlayer
            </label>
            <label style={{ marginRight: '20px' }}>
              <input
                type="radio"
                value="canvas"
                checked={playerType === 'canvas'}
                onChange={(e) => setPlayerType(e.target.value)}
                style={{ marginRight: '5px' }}
              />
              Canvas Player
            </label>
            <label>
              <input
                type="radio"
                value="whep"
                checked={playerType === 'whep'}
                onChange={(e) => setPlayerType(e.target.value)}
                style={{ marginRight: '5px' }}
              />
              WHEP Player
            </label>
          </div>
          <div style={{ border: '1px solid #ccc', borderRadius: '1px', overflow: 'hidden' }}>
            <Player
              streamName={streamName}
              playerType={playerType}
              thumbnailUrl={thumbnailMode !== 'none' ? thumbnailUrl : null}
              clickToPlay={thumbnailMode === 'click-to-play'}
              autoplayMuted={thumbnailMode === 'autoplay-muted'}
            />
          </div>
          {playerType === 'whep' && (
            <div style={{ marginTop: '10px', padding: '10px', backgroundColor: '#e3f2fd', borderRadius: '5px' }}>
              <strong>WHEP Info:</strong> This uses the standardized WebRTC-HTTP Egress Protocol for playback.
              Much simpler than WebSocket signaling - just HTTP requests!
            </div>
          )}
          {playerType === 'mist' && (
            <div style={{ marginTop: '10px', padding: '10px', backgroundColor: '#e8f5e8', borderRadius: '5px' }}>
              <strong>MistPlayer Info:</strong> This player has built-in controls, poster image support, and automatic protocol selection.
              The thumbnailUrl prop overrides the default poster image.
            </div>
          )}
          {(playerType === 'canvas' || playerType === 'whep') && thumbnailMode !== 'none' && (
            <div style={{ marginTop: '10px', padding: '10px', backgroundColor: '#fff3cd', borderRadius: '5px' }}>
              <strong>Thumbnail Mode:</strong> {thumbnailMode === 'click-to-play' ? 'Click the thumbnail to start playing' : 'Stream autoplays muted - click to unmute'}
            </div>
          )}
        </div>
      </main>
    </div>
  );
}

export default App; 