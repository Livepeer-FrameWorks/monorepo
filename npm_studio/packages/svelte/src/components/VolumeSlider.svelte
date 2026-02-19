<!--
  VolumeSlider - Reusable volume slider with popup tooltip and snap-to-100%
-->
<script lang="ts">
  interface Props {
    /** Current value (0-2 for 0-200%) */
    value: number;
    /** Callback when value changes */
    onChange: (value: number) => void;
    /** Min value (default 0) */
    min?: number;
    /** Max value (default 2 for 200%) */
    max?: number;
    /** Snap threshold around 100% (default 0.05 = 5%) */
    snapThreshold?: number;
    /** Compact mode for inline use */
    compact?: boolean;
  }

  let {
    value,
    onChange,
    min = 0,
    max = 2,
    snapThreshold = 0.05,
    compact = false,
  }: Props = $props();

  let isDragging = $state(false);
  let popupPosition = $state(0);
  let sliderRef: HTMLInputElement | undefined = $state();

  let displayValue = $derived(Math.round(value * 100));
  let isBoost = $derived(value > 1);
  let isDefault = $derived(value === 1);

  function handleInput(e: Event) {
    const target = e.target as HTMLInputElement;
    let newValue = parseInt(target.value, 10) / 100;

    // Snap to 100% if within threshold
    if (Math.abs(newValue - 1) <= snapThreshold) {
      newValue = 1;
    }

    onChange(newValue);

    // Update popup position
    if (sliderRef) {
      const rect = sliderRef.getBoundingClientRect();
      const percent = (newValue - min) / (max - min);
      popupPosition = percent * rect.width;
    }
  }

  function handleMouseDown() {
    isDragging = true;
    // Update initial position
    if (sliderRef) {
      const rect = sliderRef.getBoundingClientRect();
      const percent = (value - min) / (max - min);
      popupPosition = percent * rect.width;
    }
  }

  function handleMouseUp() {
    isDragging = false;
  }

  // Colors
  let popupColor = $derived(
    isBoost
      ? "hsl(var(--fw-sc-warning))"
      : isDefault
        ? "hsl(var(--fw-sc-success))"
        : "hsl(var(--fw-sc-accent))"
  );
  let markerColor = $derived(
    isDefault ? "hsl(var(--fw-sc-success))" : "hsl(var(--fw-sc-success) / 0.3)"
  );
</script>

<div class="volume-slider" style:min-width={compact ? "60px" : "100px"}>
  <!-- Popup tooltip -->
  {#if isDragging}
    <div class="volume-popup" style:left="{popupPosition}px" style:background={popupColor}>
      {displayValue}%{isDefault ? " (default)" : ""}
      <!-- Arrow -->
      <div class="volume-popup-arrow" style:border-top-color={popupColor}></div>
    </div>
  {/if}

  <!-- Slider track with 100% marker -->
  <div class="volume-track">
    <!-- 100% marker line -->
    <div class="volume-marker" style:left="{(1 / max) * 100}%" style:background={markerColor}></div>
    <input
      bind:this={sliderRef}
      type="range"
      min={min * 100}
      max={max * 100}
      value={Math.round(value * 100)}
      oninput={handleInput}
      onmousedown={handleMouseDown}
      onmouseup={handleMouseUp}
      onmouseleave={handleMouseUp}
      ontouchstart={handleMouseDown}
      ontouchend={handleMouseUp}
      class="volume-input"
      style:accent-color={isBoost ? "hsl(var(--fw-sc-warning))" : "hsl(var(--fw-sc-accent))"}
    />
  </div>
</div>

<style>
  .volume-slider {
    position: relative;
    flex: 1;
  }

  .volume-popup {
    position: absolute;
    bottom: 100%;
    transform: translateX(-50%);
    margin-bottom: 8px;
    padding: 4px 8px;
    color: hsl(var(--fw-sc-on-accent, 235 19% 13%));
    border-radius: 4px;
    font-size: 12px;
    font-weight: 600;
    font-family: monospace;
    white-space: nowrap;
    pointer-events: none;
    z-index: 100;
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
  }

  .volume-popup-arrow {
    position: absolute;
    top: 100%;
    left: 50%;
    transform: translateX(-50%);
    width: 0;
    height: 0;
    border-left: 6px solid transparent;
    border-right: 6px solid transparent;
    border-top: 6px solid;
  }

  .volume-track {
    position: relative;
  }

  .volume-marker {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 2px;
    border-radius: 1px;
    z-index: 1;
    pointer-events: none;
    transform: translateX(-50%);
  }

  .volume-input {
    width: 100%;
    height: 6px;
    border-radius: 3px;
    cursor: pointer;
  }
</style>
