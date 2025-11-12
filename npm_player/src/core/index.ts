/**
 * Core Player Management System
 * 
 * Exports all core functionality for the FrameWorks player system
 */

// Player selection algorithm (ported from MistMetaPlayer)
export { 
  selectPlayer, 
  type Player, 
  type PlayerSelection, 
  type SelectionOptions 
} from './selector';

// Browser and codec detection
export * from './detector';

// Scoring system
export * from './scorer';

// Player interfaces and base classes
export * from './PlayerInterface';

// Main player manager
export * from './PlayerManager';

// Player registry with all implementations
export * from './PlayerRegistry';

// Re-export for convenience
export { PlayerManager } from './PlayerManager';
export { globalPlayerManager, createPlayerManager, ensurePlayersRegistered } from './PlayerRegistry';
export type { IPlayer, PlayerOptions } from './PlayerInterface';
