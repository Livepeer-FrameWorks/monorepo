/**
 * Vanilla JS exports for non-React environments.
 *
 * @example
 * ```typescript
 * import { FrameWorksPlayer } from '@livepeer-frameworks/player-core/vanilla';
 * import '@livepeer-frameworks/player-core/player.css';
 *
 * const player = new FrameWorksPlayer('#player', {
 *   contentId: 'pk_...',
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
