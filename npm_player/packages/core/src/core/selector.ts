/**
 * Player Selection Types
 *
 * Re-exports from PlayerManager for backwards compatibility.
 * All selection logic is now consolidated in PlayerManager.
 */

export type {
  PlayerSelection,
  PlayerCombination,
  PlayerManagerOptions,
} from './PlayerManager';

// Legacy type aliases for external consumers
import type { PlayerManagerOptions } from './PlayerManager';
export type SelectionOptions = PlayerManagerOptions;
