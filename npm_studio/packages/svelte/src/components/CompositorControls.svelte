<!--
  CompositorControls Svelte Component (Compact Overlay)

  A compact floating toolbar for compositor controls.
  Designed to overlay on the video preview without taking extra space.

  Features:
  - Compact horizontal layout bar (one row)
  - Layout presets as icon buttons
  - Scaling mode as icon toggle
  - Hover to expand for more options
-->
<script lang="ts">
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

  interface Props {
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
    onCycleSourceOrder?: (direction?: 'forward' | 'backward') => void;
    currentLayout?: LayoutConfig | null;

    // Options
    showStats?: boolean;
    class?: string;
  }

  let {
    isEnabled,
    isInitialized,
    rendererType,
    stats,
    sources,
    layers,
    onLayoutApply,
    onCycleSourceOrder,
    currentLayout = null,
    showStats = true,
    class: className = '',
  }: Props = $props();

  // Layout preset definitions
  interface LayoutPresetUI {
    mode: LayoutMode;
    label: string;
    minSources: number;
  }

  const LAYOUT_PRESETS_UI: LayoutPresetUI[] = [
    { mode: 'solo', label: 'Solo', minSources: 1 },
    // 2-source layouts
    { mode: 'pip-br', label: 'PiP ↘', minSources: 2 },
    { mode: 'pip-bl', label: 'PiP ↙', minSources: 2 },
    { mode: 'pip-tr', label: 'PiP ↗', minSources: 2 },
    { mode: 'pip-tl', label: 'PiP ↖', minSources: 2 },
    { mode: 'split-h', label: 'Split ⬌', minSources: 2 },
    { mode: 'split-v', label: 'Split ⬍', minSources: 2 },
    { mode: 'focus-l', label: 'Focus ◀', minSources: 2 },
    { mode: 'focus-r', label: 'Focus ▶', minSources: 2 },
    // 3-source layouts
    { mode: 'pip-dual-br', label: 'Main+2 PiP', minSources: 3 },
    { mode: 'split-pip-r', label: 'Split+PiP', minSources: 3 },
    // Flexible layouts (2+ sources)
    { mode: 'featured', label: 'Featured', minSources: 3 },
    { mode: 'featured-r', label: 'Featured ▶', minSources: 3 },
    { mode: 'grid', label: 'Grid', minSources: 2 },
    { mode: 'stack', label: 'Stack', minSources: 2 },
  ];

  const SCALING_MODES: { mode: ScalingMode; label: string }[] = [
    { mode: 'letterbox', label: 'Letterbox (fit)' },
    { mode: 'crop', label: 'Crop (fill)' },
    { mode: 'stretch', label: 'Stretch' },
  ];

  // Tooltip state
  let tooltipText = $state<string | null>(null);
  let tooltipTarget = $state<HTMLElement | null>(null);

  function showTooltip(text: string, target: HTMLElement) {
    tooltipText = text;
    tooltipTarget = target;
  }

  function hideTooltip() {
    tooltipText = null;
    tooltipTarget = null;
  }

  function handleLayoutSelect(mode: LayoutMode, e?: MouseEvent) {
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
  }

  function handleScalingModeChange(scalingMode: ScalingMode) {
    if (!onLayoutApply || !currentLayout) return;
    onLayoutApply({ ...currentLayout, scalingMode });
  }

  // Get visibility state for each source from layers
  function getSourceVisibility(sourceId: string): boolean {
    const layer = layers.find((l) => l.sourceId === sourceId);
    return layer?.visible ?? true;
  }

  // Computed
  let visibleSourceCount = $derived(sources.filter((s) => getSourceVisibility(s.id)).length);
  let currentScalingMode = $derived(currentLayout?.scalingMode ?? 'letterbox');
  let availableLayouts = $derived(
    LAYOUT_PRESETS_UI.filter((preset) => isLayoutAvailable(preset.mode, visibleSourceCount))
  );
</script>

{#if isEnabled && isInitialized}
  <div class="fw-sc-layout-overlay {className}">
    <!-- Compact bar: Layout icons + scaling mode -->
    <div class="fw-sc-layout-bar">
      <!-- Layout section -->
      <div class="fw-sc-layout-section">
        <span class="fw-sc-layout-label">Layout</span>
        <div class="fw-sc-layout-icons">
          {#each availableLayouts as preset (preset.mode)}
            {@const isActive = currentLayout?.mode === preset.mode}
            <div
              class="fw-sc-tooltip-wrapper"
              role="presentation"
              onmouseenter={(e) => showTooltip(isActive ? `${preset.label} (click to swap)` : preset.label, e.currentTarget as HTMLElement)}
              onmouseleave={hideTooltip}
            >
              <button
                type="button"
                class="fw-sc-layout-icon {isActive ? 'fw-sc-layout-icon--active' : ''}"
                onclick={(e) => {
                  e.stopPropagation();
                  handleLayoutSelect(preset.mode, e);
                }}
              >
                {#if preset.mode === 'solo'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="10" rx="1" />
                  </svg>
                {:else if preset.mode === 'pip-br'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
                    <rect x="6.5" y="6.5" width="4" height="3" rx="0.5" />
                  </svg>
                {:else if preset.mode === 'pip-bl'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
                    <rect x="1.5" y="6.5" width="4" height="3" rx="0.5" />
                  </svg>
                {:else if preset.mode === 'pip-tr'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
                    <rect x="6.5" y="2.5" width="4" height="3" rx="0.5" />
                  </svg>
                {:else if preset.mode === 'pip-tl'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
                    <rect x="1.5" y="2.5" width="4" height="3" rx="0.5" />
                  </svg>
                {:else if preset.mode === 'split-h'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="4.5" height="10" rx="1" />
                    <rect x="6.5" y="1" width="4.5" height="10" rx="1" />
                  </svg>
                {:else if preset.mode === 'split-v'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="4.5" rx="1" />
                    <rect x="1" y="6.5" width="10" height="4.5" rx="1" />
                  </svg>
                {:else if preset.mode === 'focus-l'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="7" height="10" rx="1" />
                    <rect x="8.5" y="1" width="2.5" height="10" rx="1" fill-opacity="0.5" />
                  </svg>
                {:else if preset.mode === 'focus-r'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="2.5" height="10" rx="1" fill-opacity="0.5" />
                    <rect x="4" y="1" width="7" height="10" rx="1" />
                  </svg>
                {:else if preset.mode === 'grid'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="4.5" height="4.5" rx="1" />
                    <rect x="6.5" y="1" width="4.5" height="4.5" rx="1" />
                    <rect x="1" y="6.5" width="4.5" height="4.5" rx="1" />
                    <rect x="6.5" y="6.5" width="4.5" height="4.5" rx="1" />
                  </svg>
                {:else if preset.mode === 'stack'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="2.8" rx="0.5" />
                    <rect x="1" y="4.6" width="10" height="2.8" rx="0.5" />
                    <rect x="1" y="8.2" width="10" height="2.8" rx="0.5" />
                  </svg>
                {:else if preset.mode === 'pip-dual-br'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
                    <rect x="7" y="4" width="3.5" height="2.5" rx="0.5" />
                    <rect x="7" y="7" width="3.5" height="2.5" rx="0.5" />
                  </svg>
                {:else if preset.mode === 'split-pip-r'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="4.5" height="10" rx="1" />
                    <rect x="6.5" y="1" width="4.5" height="10" rx="1" fill-opacity="0.5" />
                    <rect x="7.5" y="7" width="2.5" height="2.5" rx="0.5" />
                  </svg>
                {:else if preset.mode === 'featured'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="7.5" rx="1" />
                    <rect x="1" y="9" width="3" height="2" rx="0.5" fill-opacity="0.5" />
                    <rect x="4.5" y="9" width="3" height="2" rx="0.5" fill-opacity="0.5" />
                    <rect x="8" y="9" width="3" height="2" rx="0.5" fill-opacity="0.5" />
                  </svg>
                {:else if preset.mode === 'featured-r'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="8" height="10" rx="1" />
                    <rect x="9.5" y="1" width="1.5" height="3" rx="0.5" fill-opacity="0.5" />
                    <rect x="9.5" y="4.5" width="1.5" height="3" rx="0.5" fill-opacity="0.5" />
                    <rect x="9.5" y="8" width="1.5" height="3" rx="0.5" fill-opacity="0.5" />
                  </svg>
                {:else}
                  <!-- Fallback icon -->
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="10" rx="1" />
                  </svg>
                {/if}
              </button>
            </div>
          {/each}
        </div>
      </div>

      <!-- Separator -->
      <div class="fw-sc-layout-separator"></div>

      <!-- Display mode section -->
      <div class="fw-sc-layout-section">
        <span class="fw-sc-layout-label">Display</span>
        <div class="fw-sc-scaling-icons">
          {#each SCALING_MODES as sm (sm.mode)}
            {@const isActive = currentScalingMode === sm.mode}
            <div
              class="fw-sc-tooltip-wrapper"
              role="presentation"
              onmouseenter={(e) => showTooltip(sm.label, e.currentTarget as HTMLElement)}
              onmouseleave={hideTooltip}
            >
              <button
                type="button"
                class="fw-sc-layout-icon {isActive ? 'fw-sc-layout-icon--active' : ''}"
                onclick={(e) => {
                  e.stopPropagation();
                  handleScalingModeChange(sm.mode);
                }}
              >
                {#if sm.mode === 'letterbox'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="3" width="10" height="6" rx="1" />
                    <rect x="0" y="1" width="12" height="1.5" fill-opacity="0.3" />
                    <rect x="0" y="9.5" width="12" height="1.5" fill-opacity="0.3" />
                  </svg>
                {:else if sm.mode === 'crop'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="0" y="0" width="12" height="12" rx="1" />
                    <path d="M2 0v2H0v1h3V0H2zM10 0v3h2V2h-2V0H9v3h3V2h-2V0h1zM0 9v1h2v2h1V9H0zM12 9H9v3h1v-2h2v-1z" fill-opacity="0.5" />
                  </svg>
                {:else if sm.mode === 'stretch'}
                  <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor">
                    <rect x="1" y="1" width="10" height="10" rx="1" fill-opacity="0.3" />
                    <path d="M3 5.5h6M3 5l-1.5 1L3 7M9 5l1.5 1L9 7M5.5 3v6M5 3L6 1.5 7 3M5 9l1 1.5 1-1.5" stroke="currentColor" stroke-width="1" fill="none" />
                  </svg>
                {/if}
              </button>
            </div>
          {/each}
        </div>
      </div>

      <!-- Stats (subtle) -->
      {#if showStats && stats}
        <div class="fw-sc-layout-separator"></div>
        <span class="fw-sc-layout-stats">
          {#if rendererType === 'webgpu'}GPU{:else if rendererType === 'webgl'}GL{:else if rendererType === 'canvas2d'}2D{/if}
          {' '}{stats.fps}fps
        </span>
      {/if}
    </div>

    <!-- Tooltip -->
    {#if tooltipText && tooltipTarget}
      <div class="fw-sc-tooltip" style="position: fixed; pointer-events: none;">
        {tooltipText}
      </div>
    {/if}
  </div>
{/if}
