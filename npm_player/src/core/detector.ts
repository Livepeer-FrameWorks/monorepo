/**
 * Browser and Codec Detection
 * Ported from MistMetaPlayer v3.1.0
 * 
 * Detects browser capabilities and codec support
 * Removes legacy IE/Flash detection code
 */

export interface BrowserInfo {
  isChrome: boolean;
  isFirefox: boolean;
  isSafari: boolean;
  isEdge: boolean;
  isAndroid: boolean;
  isIOS: boolean;
  isMobile: boolean;
  supportsMediaSource: boolean;
  supportsWebRTC: boolean;
  supportsWebSocket: boolean;
}

export interface CodecSupport {
  h264: boolean;
  h265: boolean;
  vp8: boolean;
  vp9: boolean;
  av1: boolean;
  aac: boolean;
  mp3: boolean;
  opus: boolean;
}

/**
 * Detect browser information
 */
export function getBrowserInfo(): BrowserInfo {
  const ua = navigator.userAgent.toLowerCase();
  
  return {
    isChrome: /chrome|crios/.test(ua) && !/edge|edg/.test(ua),
    isFirefox: /firefox/.test(ua),
    isSafari: /safari/.test(ua) && !/chrome|crios/.test(ua),
    isEdge: /edge|edg/.test(ua),
    isAndroid: /android/.test(ua),
    isIOS: /iphone|ipad|ipod/.test(ua),
    isMobile: /mobile|android|iphone|ipad|ipod/.test(ua),
    supportsMediaSource: 'MediaSource' in window,
    supportsWebRTC: 'RTCPeerConnection' in window,
    supportsWebSocket: 'WebSocket' in window
  };
}

/**
 * Translate track codec to codec string for MediaSource
 */
export function translateCodec(track: { codec: string; codecstring?: string; init?: string }): string {
  if (track.codecstring) {
    return track.codecstring;
  }

  const bin2hex = (index: number) => {
    if (!track.init || index >= track.init.length) return '00';
    return ('0' + track.init.charCodeAt(index).toString(16)).slice(-2);
  };

  switch (track.codec) {
    case 'AAC':
      return 'mp4a.40.2';
    case 'MP3':
      return 'mp4a.40.34';
    case 'AC3':
      return 'ec-3';
    case 'H264':
      return `avc1.${bin2hex(1)}${bin2hex(2)}${bin2hex(3)}`;
    case 'HEVC':
      return `hev1.${bin2hex(1)}${bin2hex(6)}${bin2hex(7)}${bin2hex(8)}${bin2hex(9)}${bin2hex(10)}${bin2hex(11)}${bin2hex(12)}`;
    case 'VP8':
      return 'vp8';
    case 'VP9':
      return 'vp09.00.10.08'; // Common VP9 profile
    case 'AV1':
      return 'av01.0.04M.08';
    case 'Opus':
      return 'opus';
    default:
      return track.codec.toLowerCase();
  }
}

/**
 * Test codec support using MediaSource API
 */
export function testCodecSupport(mimeType: string, codec: string): boolean {
  if (!('MediaSource' in window)) {
    return false;
  }

  if (!MediaSource.isTypeSupported) {
    return true; // Can't test, assume it works
  }

  const fullType = `${mimeType};codecs="${codec}"`;
  return MediaSource.isTypeSupported(fullType);
}

/**
 * Get comprehensive codec support info
 */
export function getCodecSupport(): CodecSupport {
  return {
    h264: testCodecSupport('video/mp4', 'avc1.42E01E'),
    h265: testCodecSupport('video/mp4', 'hev1.1.6.L93.90'),
    vp8: testCodecSupport('video/webm', 'vp8'),
    vp9: testCodecSupport('video/webm', 'vp09.00.10.08'),
    av1: testCodecSupport('video/mp4', 'av01.0.04M.08'),
    aac: testCodecSupport('video/mp4', 'mp4a.40.2'),
    mp3: testCodecSupport('audio/mpeg', 'mp3'),
    opus: testCodecSupport('audio/webm', 'opus')
  };
}

/**
 * Check if tracks are playable by testing codecs
 */
export function checkTrackPlayability(
  tracks: Array<{ type: string; codec: string; codecstring?: string; init?: string }>,
  containerType: string
): { playable: string[]; supported: string[] } {
  const playable: string[] = [];
  const supported: string[] = [];

  const tracksByType: Record<string, typeof tracks> = {};
  
  // Group tracks by type
  for (const track of tracks) {
    if (track.type === 'meta') continue;
    
    if (!tracksByType[track.type]) {
      tracksByType[track.type] = [];
    }
    tracksByType[track.type].push(track);
  }

  // Test each track type
  for (const [trackType, typeTracks] of Object.entries(tracksByType)) {
    let hasPlayableTrack = false;
    
    for (const track of typeTracks) {
      const codecString = translateCodec(track);
      if (testCodecSupport(containerType, codecString)) {
        supported.push(track.codec);
        hasPlayableTrack = true;
      }
    }
    
    if (hasPlayableTrack) {
      playable.push(trackType);
    }
  }

  return { playable, supported };
}

/**
 * Check protocol/scheme mismatch (http/https)
 */
export function checkProtocolMismatch(sourceUrl: string): boolean {
  const pageProtocol = window.location.protocol;
  const sourceProtocol = new URL(sourceUrl).protocol;
  
  // Allow file:// to access http://
  if (pageProtocol === 'file:' && sourceProtocol === 'http:') {
    return false; // No mismatch
  }
  
  return pageProtocol !== sourceProtocol;
}

/**
 * Check if current page is loaded over file://
 */
export function isFileProtocol(): boolean {
  return window.location.protocol === 'file:';
}

/**
 * Get Android version (returns null if not Android)
 */
export function getAndroidVersion(): number | null {
  const match = navigator.userAgent.match(/Android (\d+)(?:\.(\d+))?(?:\.(\d+))*/i);
  if (!match) return null;
  
  const major = parseInt(match[1], 10);
  const minor = match[2] ? parseInt(match[2], 10) : 0;
  
  return major + (minor / 10);
}

/**
 * Browser-specific compatibility checks
 */
export function getBrowserCompatibility() {
  const browser = getBrowserInfo();
  const android = getAndroidVersion();
  
  return {
    // Native HLS support
    supportsNativeHLS: browser.isSafari || browser.isIOS || (android && android >= 7),
    
    // MSE support
    supportsMSE: browser.supportsMediaSource,
    
    // WebSocket support
    supportsWebSocket: browser.supportsWebSocket,
    
    // WebRTC support
    supportsWebRTC: browser.supportsWebRTC && 'RTCRtpReceiver' in window,
    
    // Specific player recommendations
    preferVideoJs: android && android < 7, // VideoJS better for older Android
    avoidMEWSOnMac: browser.isSafari, // MEWS breaks often on Safari/macOS
    
    // File protocol limitations
    fileProtocolLimitations: isFileProtocol()
  };
}