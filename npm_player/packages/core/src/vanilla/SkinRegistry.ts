/**
 * Skin Registry — Named skin definitions with inheritance.
 *
 * A skin bundles structure, blueprints, icons, design tokens, and CSS.
 * Skins can inherit from other skins, forming a chain.
 *
 * Usage:
 * ```ts
 * import { FwSkins, registerSkin, resolveSkin } from '@livepeer-frameworks/player-core';
 *
 * registerSkin('mytheme', {
 *   inherit: 'default',
 *   blueprints: { play: (ctx) => myCustomPlayButton(ctx) },
 *   tokens: { '--fw-accent': '#ff6600' },
 * });
 * ```
 */

import type { BlueprintFactory, BlueprintMap, StructureDescriptor } from "./Blueprint";
import { DEFAULT_BLUEPRINTS } from "./defaultBlueprints";
import { DEFAULT_STRUCTURE } from "./defaultStructure";

export interface SkinDefinition {
  /** Parent skin to inherit from (default: none) */
  inherit?: string;
  /** Structure descriptor override */
  structure?: { main: StructureDescriptor };
  /** Blueprint factory overrides (merged with inherited) */
  blueprints?: Record<string, BlueprintFactory>;
  /** Icon overrides (SVG strings) */
  icons?: Record<string, { svg: string; size?: number }>;
  /** CSS custom property (design token) overrides */
  tokens?: Record<string, string>;
  /** Extra CSS to inject */
  css?: { skin?: string };
}

export interface ResolvedSkin {
  structure: StructureDescriptor;
  blueprints: BlueprintMap;
  icons: Record<string, { svg: string; size?: number }>;
  tokens: Record<string, string>;
  css: string;
}

/** Global skin registry */
export const FwSkins: Record<string, SkinDefinition> = {};

/** Register a named skin */
export function registerSkin(name: string, definition: SkinDefinition): void {
  FwSkins[name] = definition;
}

/** Resolve a skin by walking its inheritance chain */
export function resolveSkin(name: string): ResolvedSkin {
  const visited = new Set<string>();
  const chain: SkinDefinition[] = [];

  let current: string | undefined = name;
  while (current) {
    if (visited.has(current)) {
      console.warn(`[SkinRegistry] Circular inheritance detected at "${current}"`);
      break;
    }
    visited.add(current);
    const def: SkinDefinition | undefined = FwSkins[current];
    if (!def) break;
    chain.unshift(def);
    current = def.inherit;
  }

  // Start with defaults, then layer each skin in order
  const result: ResolvedSkin = {
    structure: DEFAULT_STRUCTURE,
    blueprints: { ...DEFAULT_BLUEPRINTS },
    icons: {},
    tokens: {},
    css: "",
  };

  for (const skin of chain) {
    if (skin.structure?.main) {
      result.structure = skin.structure.main;
    }
    if (skin.blueprints) {
      Object.assign(result.blueprints, skin.blueprints);
    }
    if (skin.icons) {
      Object.assign(result.icons, skin.icons);
    }
    if (skin.tokens) {
      Object.assign(result.tokens, skin.tokens);
    }
    if (skin.css?.skin) {
      result.css += skin.css.skin + "\n";
    }
  }

  return result;
}

// Register the built-in 'default' skin
registerSkin("default", {
  structure: { main: DEFAULT_STRUCTURE },
  blueprints: DEFAULT_BLUEPRINTS,
});
