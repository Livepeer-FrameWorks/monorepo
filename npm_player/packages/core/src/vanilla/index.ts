/**
 * Vanilla JS exports for non-React environments.
 *
 * @example
 * ```typescript
 * import { FrameWorksPlayer } from '@livepeer-frameworks/player/vanilla';
 * import '@livepeer-frameworks/player/player.css';
 *
 * const player = new FrameWorksPlayer('#player', {
 *   contentId: 'my-stream',
 *   contentType: 'live',
 *   gatewayUrl: 'https://gateway.example.com/graphql',
 * });
 * ```
 */

export { FrameWorksPlayer, default } from './FrameWorksPlayer';
export type { FrameWorksPlayerOptions } from './FrameWorksPlayer';

// Re-export useful types from core
export type { PlayerControllerEvents } from '../core/PlayerController';
export type { PlayerState, PlayerStateContext, StreamState, ContentEndpoints } from '../types';
