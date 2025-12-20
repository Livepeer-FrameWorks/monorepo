/**
 * Player Implementations
 *
 * Framework-agnostic player implementations that extend BasePlayer.
 * These are used by PlayerRegistry for dynamic player selection.
 */

// Direct playback players
export { NativePlayerImpl, DirectPlaybackPlayerImpl } from './NativePlayer';

// Adaptive streaming players
export { HlsJsPlayerImpl } from './HlsJsPlayer';
export { DashJsPlayerImpl } from './DashJsPlayer';
export { VideoJsPlayerImpl } from './VideoJsPlayer';

// MistServer-specific players
export { MistPlayerImpl } from './MistPlayer';
export { MewsWsPlayerImpl } from './MewsWsPlayer';
export { MistWebRTCPlayerImpl } from './MistWebRTCPlayer';

// Low-latency WebCodecs player (WebSocket + WebCodecs API)
export { WebCodecsPlayerImpl } from './WebCodecsPlayer';
