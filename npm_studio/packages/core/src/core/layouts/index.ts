/**
 * Layout Strategies
 *
 * Preset layout configurations for common streaming scenarios.
 * Layouts calculate layer transforms automatically based on source arrangement.
 *
 * Available layouts:
 * - Solo: Single source fullscreen
 * - PiP (4 corners): Main source with smaller overlay in corner
 * - Split H/V: Two sources side by side (50/50)
 * - Focus L/R: Two sources with emphasis (70/30)
 * - Grid: 2x2 grid for 3-4 sources
 * - Stack: Vertical stack of sources
 */

import type { Layer, LayoutConfig, LayoutMode, ScalingMode } from "../../types";
import { DEFAULT_LAYER_TRANSFORM } from "../../types";

// ============================================================================
// Layout Preset Definitions
// ============================================================================

export interface LayoutPreset {
  mode: LayoutMode;
  label: string;
  icon: string;
  minSources: number;
  maxSources: number;
}

/**
 * All available layout presets with metadata
 */
export const LAYOUT_PRESETS: LayoutPreset[] = [
  { mode: "solo", label: "Solo", icon: "⬜", minSources: 1, maxSources: 1 },
  // 2-source layouts
  { mode: "pip-br", label: "PiP ↘", icon: "◳", minSources: 2, maxSources: 2 },
  { mode: "pip-bl", label: "PiP ↙", icon: "◲", minSources: 2, maxSources: 2 },
  { mode: "pip-tr", label: "PiP ↗", icon: "◱", minSources: 2, maxSources: 2 },
  { mode: "pip-tl", label: "PiP ↖", icon: "◰", minSources: 2, maxSources: 2 },
  { mode: "split-h", label: "Split ⬌", icon: "▥", minSources: 2, maxSources: 2 },
  { mode: "split-v", label: "Split ⬍", icon: "▤", minSources: 2, maxSources: 2 },
  { mode: "focus-l", label: "Focus ◀", icon: "◧", minSources: 2, maxSources: 2 },
  { mode: "focus-r", label: "Focus ▶", icon: "◨", minSources: 2, maxSources: 2 },
  // 3-source layouts
  { mode: "pip-dual-br", label: "Main+2 PiP", icon: "⊞", minSources: 3, maxSources: 3 },
  { mode: "pip-dual-bl", label: "Main+2 PiP ↙", icon: "⊟", minSources: 3, maxSources: 3 },
  { mode: "split-pip-l", label: "Split+PiP", icon: "⊠", minSources: 3, maxSources: 3 },
  { mode: "split-pip-r", label: "Split+PiP ▶", icon: "⊡", minSources: 3, maxSources: 3 },
  // Featured layout (1 main + strip of others)
  { mode: "featured", label: "Featured", icon: "⬒", minSources: 3, maxSources: 99 },
  { mode: "featured-r", label: "Featured ▶", icon: "⬓", minSources: 3, maxSources: 99 },
  // Auto-grid (works with any count 2+)
  { mode: "grid", label: "Grid", icon: "▦", minSources: 2, maxSources: 99 },
  { mode: "stack", label: "Stack", icon: "☰", minSources: 2, maxSources: 99 },
];

// ============================================================================
// Layer Creation Helpers
// ============================================================================

// Default scaling mode used when not specified
let currentScalingMode: ScalingMode = "letterbox";

function createLayer(
  sourceId: string,
  zIndex: number,
  transform: Partial<typeof DEFAULT_LAYER_TRANSFORM>,
  scalingMode: ScalingMode = currentScalingMode
): Layer {
  return {
    id: `layer-${zIndex}-${sourceId}`,
    sourceId,
    visible: true,
    locked: false,
    zIndex,
    transform: { ...DEFAULT_LAYER_TRANSFORM, ...transform },
    scalingMode,
  };
}

// ============================================================================
// Layout Implementations
// ============================================================================

const PIP_SCALE = 0.25;
const PIP_PADDING = 0.02;
const PIP_BORDER_RADIUS = 8;
const SPLIT_GAP = 0.005;

/**
 * Solo layout - single source fullscreen
 */
function applySoloLayout(sourceIds: string[]): Layer[] {
  return [createLayer(sourceIds[0], 0, { x: 0, y: 0, width: 1, height: 1 })];
}

/**
 * PiP layout - main source with overlay in specified corner
 */
function applyPipLayout(
  sourceIds: string[],
  corner: "tl" | "tr" | "bl" | "br",
  pipScale: number = PIP_SCALE
): Layer[] {
  if (sourceIds.length < 2) return applySoloLayout(sourceIds);

  const positions = {
    tl: { x: PIP_PADDING, y: PIP_PADDING },
    tr: { x: 1 - pipScale - PIP_PADDING, y: PIP_PADDING },
    bl: { x: PIP_PADDING, y: 1 - pipScale - PIP_PADDING },
    br: { x: 1 - pipScale - PIP_PADDING, y: 1 - pipScale - PIP_PADDING },
  };

  const pos = positions[corner];

  return [
    // Main (background)
    createLayer(sourceIds[0], 0, { x: 0, y: 0, width: 1, height: 1 }),
    // PiP (overlay)
    createLayer(sourceIds[1], 1, {
      x: pos.x,
      y: pos.y,
      width: pipScale,
      height: pipScale,
      borderRadius: PIP_BORDER_RADIUS,
    }),
  ];
}

/**
 * Split layout - two sources side by side
 */
function applySplitLayout(sourceIds: string[], direction: "h" | "v", ratio: number = 0.5): Layer[] {
  if (sourceIds.length < 2) return applySoloLayout(sourceIds);

  const gap = SPLIT_GAP;
  const halfGap = gap / 2;

  if (direction === "h") {
    // Horizontal split (left-right)
    return [
      createLayer(sourceIds[0], 0, {
        x: 0,
        y: 0,
        width: ratio - halfGap,
        height: 1,
      }),
      createLayer(sourceIds[1], 0, {
        x: ratio + halfGap,
        y: 0,
        width: 1 - ratio - halfGap,
        height: 1,
      }),
    ];
  } else {
    // Vertical split (top-bottom)
    return [
      createLayer(sourceIds[0], 0, {
        x: 0,
        y: 0,
        width: 1,
        height: ratio - halfGap,
      }),
      createLayer(sourceIds[1], 0, {
        x: 0,
        y: ratio + halfGap,
        width: 1,
        height: 1 - ratio - halfGap,
      }),
    ];
  }
}

/**
 * Grid layout - auto-scaling grid for any number of sources
 * Calculates optimal grid dimensions based on source count
 */
function applyGridLayout(sourceIds: string[]): Layer[] {
  const count = sourceIds.length;
  if (count === 0) return [];
  if (count === 1) return applySoloLayout(sourceIds);
  if (count === 2) return applySplitLayout(sourceIds, "h");

  const gap = SPLIT_GAP;

  // Calculate optimal grid dimensions
  // For count <= 4: 2x2, for 5-6: 3x2, for 7-9: 3x3, etc.
  const cols = Math.ceil(Math.sqrt(count));
  const rows = Math.ceil(count / cols);

  const cellW = (1 - gap * (cols - 1)) / cols;
  const cellH = (1 - gap * (rows - 1)) / rows;

  const layers: Layer[] = [];

  for (let i = 0; i < count; i++) {
    const col = i % cols;
    const row = Math.floor(i / cols);

    // Center the last row if it's not full
    const isLastRow = row === rows - 1;
    const itemsInRow = isLastRow ? ((count - 1) % cols) + 1 : cols;
    let offsetX = 0;

    if (isLastRow && itemsInRow < cols) {
      // Center the incomplete row
      const rowWidth = itemsInRow * cellW + (itemsInRow - 1) * gap;
      offsetX = (1 - rowWidth) / 2;
    }

    const x = offsetX + col * (cellW + gap);
    const y = row * (cellH + gap);

    layers.push(createLayer(sourceIds[i], i, { x, y, width: cellW, height: cellH }));
  }

  return layers;
}

/**
 * Dual PiP layout - main source with 2 PiP overlays in corner
 */
function applyDualPipLayout(sourceIds: string[], corner: "br" | "bl"): Layer[] {
  if (sourceIds.length < 3) return applyPipLayout(sourceIds, corner === "br" ? "br" : "bl");

  const pipScale = PIP_SCALE;
  const pipGap = 0.01;

  // Main source fullscreen
  const layers: Layer[] = [createLayer(sourceIds[0], 0, { x: 0, y: 0, width: 1, height: 1 })];

  if (corner === "br") {
    // Two PiPs stacked vertically in bottom-right
    layers.push(
      createLayer(sourceIds[1], 1, {
        x: 1 - pipScale - PIP_PADDING,
        y: 1 - 2 * pipScale - pipGap - PIP_PADDING,
        width: pipScale,
        height: pipScale,
        borderRadius: PIP_BORDER_RADIUS,
      }),
      createLayer(sourceIds[2], 2, {
        x: 1 - pipScale - PIP_PADDING,
        y: 1 - pipScale - PIP_PADDING,
        width: pipScale,
        height: pipScale,
        borderRadius: PIP_BORDER_RADIUS,
      })
    );
  } else {
    // Two PiPs stacked vertically in bottom-left
    layers.push(
      createLayer(sourceIds[1], 1, {
        x: PIP_PADDING,
        y: 1 - 2 * pipScale - pipGap - PIP_PADDING,
        width: pipScale,
        height: pipScale,
        borderRadius: PIP_BORDER_RADIUS,
      }),
      createLayer(sourceIds[2], 2, {
        x: PIP_PADDING,
        y: 1 - pipScale - PIP_PADDING,
        width: pipScale,
        height: pipScale,
        borderRadius: PIP_BORDER_RADIUS,
      })
    );
  }

  return layers;
}

/**
 * Split + PiP layout - two sources side by side, third as PiP overlay
 */
function applySplitPipLayout(sourceIds: string[], pipSide: "l" | "r"): Layer[] {
  if (sourceIds.length < 3) return applySplitLayout(sourceIds, "h");

  const gap = SPLIT_GAP;
  const halfGap = gap / 2;
  const pipScale = PIP_SCALE;

  const layers: Layer[] = [
    // Left half
    createLayer(sourceIds[0], 0, { x: 0, y: 0, width: 0.5 - halfGap, height: 1 }),
    // Right half
    createLayer(sourceIds[1], 0, { x: 0.5 + halfGap, y: 0, width: 0.5 - halfGap, height: 1 }),
  ];

  // PiP overlay
  if (pipSide === "l") {
    // PiP on left half, bottom-right of that section
    layers.push(
      createLayer(sourceIds[2], 1, {
        x: 0.5 - halfGap - pipScale - PIP_PADDING,
        y: 1 - pipScale - PIP_PADDING,
        width: pipScale,
        height: pipScale,
        borderRadius: PIP_BORDER_RADIUS,
      })
    );
  } else {
    // PiP on right half, bottom-right of that section
    layers.push(
      createLayer(sourceIds[2], 1, {
        x: 1 - pipScale - PIP_PADDING,
        y: 1 - pipScale - PIP_PADDING,
        width: pipScale,
        height: pipScale,
        borderRadius: PIP_BORDER_RADIUS,
      })
    );
  }

  return layers;
}

/**
 * Featured layout - one main source large, others in a strip
 * @param direction - 'bottom' for strip below, 'right' for strip on right
 */
function applyFeaturedLayout(sourceIds: string[], direction: "bottom" | "right"): Layer[] {
  const count = sourceIds.length;
  if (count <= 2) return applySplitLayout(sourceIds, direction === "bottom" ? "v" : "h", 0.75);

  const gap = SPLIT_GAP;
  const stripRatio = 0.2; // Strip takes 20% of the space
  const mainRatio = 1 - stripRatio - gap;

  const layers: Layer[] = [];

  if (direction === "bottom") {
    // Main source on top (80%)
    layers.push(
      createLayer(sourceIds[0], 0, {
        x: 0,
        y: 0,
        width: 1,
        height: mainRatio,
      })
    );

    // Strip of thumbnails at bottom
    const thumbCount = count - 1;
    const thumbW = (1 - gap * (thumbCount - 1)) / thumbCount;

    for (let i = 1; i < count; i++) {
      layers.push(
        createLayer(sourceIds[i], 0, {
          x: (i - 1) * (thumbW + gap),
          y: mainRatio + gap,
          width: thumbW,
          height: stripRatio,
        })
      );
    }
  } else {
    // Main source on left (80%)
    layers.push(
      createLayer(sourceIds[0], 0, {
        x: 0,
        y: 0,
        width: mainRatio,
        height: 1,
      })
    );

    // Strip of thumbnails on right
    const thumbCount = count - 1;
    const thumbH = (1 - gap * (thumbCount - 1)) / thumbCount;

    for (let i = 1; i < count; i++) {
      layers.push(
        createLayer(sourceIds[i], 0, {
          x: mainRatio + gap,
          y: (i - 1) * (thumbH + gap),
          width: stripRatio,
          height: thumbH,
        })
      );
    }
  }

  return layers;
}

/**
 * Stack layout - vertical stack of sources (no limit)
 */
function applyStackLayout(sourceIds: string[]): Layer[] {
  const count = sourceIds.length;
  if (count === 0) return [];
  if (count === 1) return applySoloLayout(sourceIds);

  const gap = SPLIT_GAP;
  const cellH = (1 - gap * (count - 1)) / count;

  return sourceIds.map((id, i) =>
    createLayer(id, 0, {
      x: 0,
      y: i * (cellH + gap),
      width: 1,
      height: cellH,
    })
  );
}

// ============================================================================
// Main Layout Factory
// ============================================================================

/**
 * Apply a layout configuration to a list of sources
 *
 * @param layout - Layout configuration
 * @param sourceIds - Array of source IDs to arrange
 * @returns Array of Layer configurations
 */
export function applyLayout(layout: LayoutConfig, sourceIds: string[]): Layer[] {
  if (sourceIds.length === 0) {
    return [];
  }

  // Set the scaling mode for all layers created by this layout
  currentScalingMode = layout.scalingMode ?? "letterbox";
  const pipScale = layout.pipScale ?? PIP_SCALE;
  const splitRatio = layout.splitRatio ?? 0.5;

  switch (layout.mode) {
    // Solo / Fullscreen
    case "solo":
    case "fullscreen":
      return applySoloLayout(sourceIds);

    // 2-source PiP variants
    case "pip-br":
    case "pip":
      return applyPipLayout(sourceIds, "br", pipScale);
    case "pip-bl":
      return applyPipLayout(sourceIds, "bl", pipScale);
    case "pip-tr":
      return applyPipLayout(sourceIds, "tr", pipScale);
    case "pip-tl":
      return applyPipLayout(sourceIds, "tl", pipScale);

    // Split variants
    case "split-h":
    case "side-by-side":
      return applySplitLayout(sourceIds, "h", splitRatio);
    case "split-v":
      return applySplitLayout(sourceIds, "v", splitRatio);

    // Focus variants (70/30 split)
    case "focus-l":
      return applySplitLayout(sourceIds, "h", 0.7);
    case "focus-r":
      return applySplitLayout(sourceIds, "h", 0.3);

    // 3-source layouts
    case "pip-dual-br":
      return applyDualPipLayout(sourceIds, "br");
    case "pip-dual-bl":
      return applyDualPipLayout(sourceIds, "bl");
    case "split-pip-l":
      return applySplitPipLayout(sourceIds, "l");
    case "split-pip-r":
      return applySplitPipLayout(sourceIds, "r");

    // Featured layouts (any source count 3+)
    case "featured":
      return applyFeaturedLayout(sourceIds, "bottom");
    case "featured-r":
      return applyFeaturedLayout(sourceIds, "right");

    // Multi-source layouts (auto-grid for any count)
    case "grid":
      return applyGridLayout(sourceIds);
    case "stack":
      return applyStackLayout(sourceIds);

    default:
      return applySoloLayout(sourceIds);
  }
}

// ============================================================================
// Utility Functions
// ============================================================================

/**
 * Create a default LayoutConfig
 */
export function createDefaultLayoutConfig(): LayoutConfig {
  return {
    mode: "solo",
    scalingMode: "letterbox",
  };
}

/**
 * Get all available layout presets
 */
export function getLayoutPresets(): LayoutPreset[] {
  return LAYOUT_PRESETS;
}

/**
 * Get presets that work with the given source count
 */
export function getAvailablePresets(sourceCount: number): LayoutPreset[] {
  return LAYOUT_PRESETS.filter((p) => sourceCount >= p.minSources && sourceCount <= p.maxSources);
}

/**
 * Get minimum source count required for a layout mode
 */
export function getMinSourcesForLayout(mode: LayoutMode): number {
  const preset = LAYOUT_PRESETS.find((p) => p.mode === mode);
  return preset?.minSources ?? 1;
}

/**
 * Check if a layout mode is available for a given source count
 */
export function isLayoutAvailable(mode: LayoutMode, sourceCount: number): boolean {
  const preset = LAYOUT_PRESETS.find((p) => p.mode === mode);
  if (!preset) return false;
  return sourceCount >= preset.minSources && sourceCount <= preset.maxSources;
}

// ============================================================================
// Legacy exports for backwards compatibility
// ============================================================================

export { applySoloLayout as applyFullscreenLayout };
export { applyPipLayout };
export { applySplitLayout as applySideBySideLayout };

export function createPipLayoutConfig(
  pipPosition: "top-left" | "top-right" | "bottom-left" | "bottom-right" = "bottom-right",
  pipScale: number = 0.25
): LayoutConfig {
  const positionMap = {
    "top-left": "pip-tl",
    "top-right": "pip-tr",
    "bottom-left": "pip-bl",
    "bottom-right": "pip-br",
  } as const;
  return {
    mode: positionMap[pipPosition],
    pipScale,
  };
}

export function createSideBySideLayoutConfig(
  splitRatio: number = 0.5,
  splitDirection: "horizontal" | "vertical" = "horizontal"
): LayoutConfig {
  return {
    mode: splitDirection === "horizontal" ? "split-h" : "split-v",
    splitRatio,
  };
}

export function getAvailableLayoutModes(): LayoutMode[] {
  return LAYOUT_PRESETS.map((p) => p.mode);
}
