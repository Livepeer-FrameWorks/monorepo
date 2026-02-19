/**
 * Vanilla JS exports for non-React environments.
 *
 * @example
 * ```typescript
 * import { createPlayer } from '@livepeer-frameworks/player-core/vanilla';
 * import '@livepeer-frameworks/player-core/player.css';
 *
 * const player = createPlayer({
 *   target: '#player',
 *   contentId: 'my-stream',
 *   gatewayUrl: 'https://gateway.example.com/graphql',
 * });
 * ```
 */

export { FrameWorksPlayer, default } from "./FrameWorksPlayer";
export type { FrameWorksPlayerOptions } from "./FrameWorksPlayer";

export { createPlayer } from "./createPlayer";
export type { CreatePlayerConfig, PlayerInstance, PlayerCapabilities } from "./createPlayer";

// Reactive state (per-property subscriptions)
export { createReactiveState } from "./ReactiveState";
export type { ReactiveState, ReactiveStateProperty } from "./ReactiveState";

// Blueprint system
export type {
  BlueprintContext,
  BlueprintFactory,
  BlueprintMap,
  StructureDescriptor,
} from "./Blueprint";
export { DEFAULT_BLUEPRINTS } from "./defaultBlueprints";
export { DEFAULT_STRUCTURE } from "./defaultStructure";
export { buildStructure } from "./StructureBuilder";

// Skin registry
export { FwSkins, registerSkin, resolveSkin } from "./SkinRegistry";
export type { SkinDefinition, ResolvedSkin } from "./SkinRegistry";

// Simplified player registration
export { registerPlayer } from "./registerPlayer";
export type { SimplePlayerDefinition } from "./registerPlayer";

// Re-export useful types from core
export type { PlayerControllerEvents } from "../core/PlayerController";
export type { PlayerState, PlayerStateContext, StreamState, ContentEndpoints } from "../types";
