/**
 * CompositorControls Component (Compact Overlay)
 *
 * A compact floating toolbar for compositor controls.
 * Designed to overlay on the video preview without taking extra space.
 *
 * Features:
 * - Compact horizontal layout bar (one row)
 * - Layout presets as icon buttons
 * - Scaling mode as icon toggle
 * - Hover to expand for more options
 */

import React, { useCallback, useState } from 'react';
import type {
  LayoutConfig,
  LayoutMode,
  ScalingMode,
  MediaSource,
  RendererType,
  RendererStats,
  Layer,
} from '@livepeer-frameworks/streamcrafter-core';
import { isLayoutAvailable } from '@livepeer-frameworks/streamcrafter-core';

// ============================================================================
// Custom Tooltip Component (instant, styled)
// ============================================================================

interface TooltipProps {
  text: string;
  children: React.ReactNode;
}

const Tooltip: React.FC<TooltipProps> = ({ text, children }) => {
  const [show, setShow] = useState(false);

  return (
    <div
      className="fw-sc-tooltip-wrapper"
      onMouseEnter={() => setShow(true)}
      onMouseLeave={() => setShow(false)}
    >
      {children}
      {show && <div className="fw-sc-tooltip">{text}</div>}
    </div>
  );
};

export interface CompositorControlsProps {
  // State
  isEnabled: boolean;
  isInitialized: boolean;
  rendererType: RendererType | null;
  stats: RendererStats | null;

  // Sources and layers
  sources: MediaSource[];
  layers: Layer[];

  // Actions
  onLayoutApply?: (layout: LayoutConfig) => void;
  onCycleSourceOrder?: (direction?: 'forward' | 'backward') => void;  // Called when clicking active layout
  currentLayout?: LayoutConfig | null;

  // Options
  showStats?: boolean;
  className?: string;
}

// ============================================================================
// Compact SVG Icons (12x12)
// ============================================================================

function SoloIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="10" rx="1" />
    </svg>
  );
}

function PipBRIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="10" rx="1" fillOpacity="0.3" />
      <rect x="6.5" y="6.5" width="4" height="3" rx="0.5" />
    </svg>
  );
}

function PipBLIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="10" rx="1" fillOpacity="0.3" />
      <rect x="1.5" y="6.5" width="4" height="3" rx="0.5" />
    </svg>
  );
}

function PipTRIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="10" rx="1" fillOpacity="0.3" />
      <rect x="6.5" y="2.5" width="4" height="3" rx="0.5" />
    </svg>
  );
}

function PipTLIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="10" rx="1" fillOpacity="0.3" />
      <rect x="1.5" y="2.5" width="4" height="3" rx="0.5" />
    </svg>
  );
}

function SplitHIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="4.5" height="10" rx="1" />
      <rect x="6.5" y="1" width="4.5" height="10" rx="1" />
    </svg>
  );
}

function SplitVIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="4.5" rx="1" />
      <rect x="1" y="6.5" width="10" height="4.5" rx="1" />
    </svg>
  );
}

function FocusLIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="7" height="10" rx="1" />
      <rect x="8.5" y="1" width="2.5" height="10" rx="1" fillOpacity="0.5" />
    </svg>
  );
}

function FocusRIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="2.5" height="10" rx="1" fillOpacity="0.5" />
      <rect x="4" y="1" width="7" height="10" rx="1" />
    </svg>
  );
}

function GridIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="4.5" height="4.5" rx="1" />
      <rect x="6.5" y="1" width="4.5" height="4.5" rx="1" />
      <rect x="1" y="6.5" width="4.5" height="4.5" rx="1" />
      <rect x="6.5" y="6.5" width="4.5" height="4.5" rx="1" />
    </svg>
  );
}

function StackIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="2.8" rx="0.5" />
      <rect x="1" y="4.6" width="10" height="2.8" rx="0.5" />
      <rect x="1" y="8.2" width="10" height="2.8" rx="0.5" />
    </svg>
  );
}

// 3-source layout icons
function DualPipIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="10" rx="1" fillOpacity="0.3" />
      <rect x="7" y="4" width="3.5" height="2.5" rx="0.5" />
      <rect x="7" y="7" width="3.5" height="2.5" rx="0.5" />
    </svg>
  );
}

function SplitPipIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="4.5" height="10" rx="1" />
      <rect x="6.5" y="1" width="4.5" height="10" rx="1" fillOpacity="0.5" />
      <rect x="7.5" y="7" width="2.5" height="2.5" rx="0.5" />
    </svg>
  );
}

function FeaturedIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="7.5" rx="1" />
      <rect x="1" y="9" width="3" height="2" rx="0.5" fillOpacity="0.5" />
      <rect x="4.5" y="9" width="3" height="2" rx="0.5" fillOpacity="0.5" />
      <rect x="8" y="9" width="3" height="2" rx="0.5" fillOpacity="0.5" />
    </svg>
  );
}

function FeaturedRIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="8" height="10" rx="1" />
      <rect x="9.5" y="1" width="1.5" height="3" rx="0.5" fillOpacity="0.5" />
      <rect x="9.5" y="4.5" width="1.5" height="3" rx="0.5" fillOpacity="0.5" />
      <rect x="9.5" y="8" width="1.5" height="3" rx="0.5" fillOpacity="0.5" />
    </svg>
  );
}

// Scaling mode icons
function LetterboxIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="3" width="10" height="6" rx="1" />
      <rect x="0" y="1" width="12" height="1.5" fillOpacity="0.3" />
      <rect x="0" y="9.5" width="12" height="1.5" fillOpacity="0.3" />
    </svg>
  );
}

function CropIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="0" y="0" width="12" height="12" rx="1" />
      <path d="M2 0v2H0v1h3V0H2zM10 0v3h2V2h-2V0H9v3h3V2h-2V0h1zM0 9v1h2v2h1V9H0zM12 9H9v3h1v-2h2v-1z" fillOpacity="0.5" />
    </svg>
  );
}

function StretchIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
      <rect x="1" y="1" width="10" height="10" rx="1" fillOpacity="0.3" />
      <path d="M3 5.5h6M3 5l-1.5 1L3 7M9 5l1.5 1L9 7M5.5 3v6M5 3L6 1.5 7 3M5 9l1 1.5 1-1.5" stroke="currentColor" strokeWidth="1" fill="none" />
    </svg>
  );
}

// ============================================================================
// Layout Preset Definitions
// ============================================================================

interface LayoutPresetUI {
  mode: LayoutMode;
  label: string;
  icon: React.ReactNode;
  minSources: number;
}

const LAYOUT_PRESETS_UI: LayoutPresetUI[] = [
  { mode: 'solo', label: 'Solo', icon: <SoloIcon />, minSources: 1 },
  // 2-source layouts
  { mode: 'pip-br', label: 'PiP ↘', icon: <PipBRIcon />, minSources: 2 },
  { mode: 'pip-bl', label: 'PiP ↙', icon: <PipBLIcon />, minSources: 2 },
  { mode: 'pip-tr', label: 'PiP ↗', icon: <PipTRIcon />, minSources: 2 },
  { mode: 'pip-tl', label: 'PiP ↖', icon: <PipTLIcon />, minSources: 2 },
  { mode: 'split-h', label: 'Split ⬌', icon: <SplitHIcon />, minSources: 2 },
  { mode: 'split-v', label: 'Split ⬍', icon: <SplitVIcon />, minSources: 2 },
  { mode: 'focus-l', label: 'Focus ◀', icon: <FocusLIcon />, minSources: 2 },
  { mode: 'focus-r', label: 'Focus ▶', icon: <FocusRIcon />, minSources: 2 },
  // 3-source layouts
  { mode: 'pip-dual-br', label: 'Main+2 PiP', icon: <DualPipIcon />, minSources: 3 },
  { mode: 'split-pip-r', label: 'Split+PiP', icon: <SplitPipIcon />, minSources: 3 },
  // Flexible layouts (2+ sources)
  { mode: 'featured', label: 'Featured', icon: <FeaturedIcon />, minSources: 3 },
  { mode: 'featured-r', label: 'Featured ▶', icon: <FeaturedRIcon />, minSources: 3 },
  { mode: 'grid', label: 'Grid', icon: <GridIcon />, minSources: 2 },
  { mode: 'stack', label: 'Stack', icon: <StackIcon />, minSources: 2 },
];

const SCALING_MODES: { mode: ScalingMode; icon: React.ReactNode; label: string }[] = [
  { mode: 'letterbox', icon: <LetterboxIcon />, label: 'Letterbox (fit)' },
  { mode: 'crop', icon: <CropIcon />, label: 'Crop (fill)' },
  { mode: 'stretch', icon: <StretchIcon />, label: 'Stretch' },
];

// ============================================================================
// Main Component
// ============================================================================

export function CompositorControls({
  isEnabled,
  isInitialized,
  rendererType,
  stats,
  sources,
  layers,
  onLayoutApply,
  onCycleSourceOrder,
  currentLayout,
  showStats = true,
  className = '',
}: CompositorControlsProps) {
  const handleLayoutSelect = useCallback(
    (mode: LayoutMode, e?: React.MouseEvent) => {
      // If clicking the already-active layout, cycle source order
      if (currentLayout?.mode === mode && onCycleSourceOrder) {
        const direction = e?.shiftKey ? 'backward' : 'forward';
        onCycleSourceOrder(direction);
        return;
      }

      if (!onLayoutApply) return;
      const layout: LayoutConfig = {
        mode,
        scalingMode: currentLayout?.scalingMode ?? 'letterbox',
        pipScale: 0.25,
      };
      onLayoutApply(layout);
    },
    [onLayoutApply, onCycleSourceOrder, currentLayout?.mode, currentLayout?.scalingMode]
  );

  const handleScalingModeChange = useCallback(
    (scalingMode: ScalingMode) => {
      if (!onLayoutApply || !currentLayout) return;
      onLayoutApply({ ...currentLayout, scalingMode });
    },
    [onLayoutApply, currentLayout]
  );

  // Don't render if not enabled/initialized
  if (!isEnabled || !isInitialized) {
    return null;
  }

  // Get visibility state for each source from layers
  const getSourceVisibility = (sourceId: string): boolean => {
    const layer = layers.find((l) => l.sourceId === sourceId);
    return layer?.visible ?? true;
  };

  const visibleSourceCount = sources.filter((s) => getSourceVisibility(s.id)).length;
  const currentScalingMode = currentLayout?.scalingMode ?? 'letterbox';

  // Filter to only show available layouts based on source count
  const availableLayouts = LAYOUT_PRESETS_UI.filter(
    (preset) => isLayoutAvailable(preset.mode, visibleSourceCount)
  );

  return (
    <div className={`fw-sc-layout-overlay ${className}`}>
      {/* Compact bar: Layout icons + scaling mode */}
      <div className="fw-sc-layout-bar">
        {/* Layout section */}
        <div className="fw-sc-layout-section">
          <span className="fw-sc-layout-label">Layout</span>
          <div className="fw-sc-layout-icons">
            {availableLayouts.map((preset) => {
              const isActive = currentLayout?.mode === preset.mode;
              return (
                <Tooltip key={preset.mode} text={isActive ? `${preset.label} (click to swap)` : preset.label}>
                  <button
                    type="button"
                    className={`fw-sc-layout-icon ${isActive ? 'fw-sc-layout-icon--active' : ''}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      handleLayoutSelect(preset.mode, e);
                    }}
                  >
                    {preset.icon}
                  </button>
                </Tooltip>
              );
            })}
          </div>
        </div>

        {/* Separator */}
        <div className="fw-sc-layout-separator" />

        {/* Display mode section */}
        <div className="fw-sc-layout-section">
          <span className="fw-sc-layout-label">Display</span>
          <div className="fw-sc-scaling-icons">
            {SCALING_MODES.map((sm) => {
              const isActive = currentScalingMode === sm.mode;
              return (
                <Tooltip key={sm.mode} text={sm.label}>
                  <button
                    type="button"
                    className={`fw-sc-layout-icon ${isActive ? 'fw-sc-layout-icon--active' : ''}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      handleScalingModeChange(sm.mode);
                    }}
                  >
                    {sm.icon}
                  </button>
                </Tooltip>
              );
            })}
          </div>
        </div>

        {/* Stats (subtle) */}
        {showStats && stats && (
          <>
            <div className="fw-sc-layout-separator" />
            <span className="fw-sc-layout-stats">
              {rendererType === 'webgpu' && 'GPU'}
              {rendererType === 'webgl' && 'GL'}
              {rendererType === 'canvas2d' && '2D'}
              {' '}{stats.fps}fps
            </span>
          </>
        )}
      </div>
    </div>
  );
}

export default CompositorControls;
