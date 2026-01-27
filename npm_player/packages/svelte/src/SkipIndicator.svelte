<script lang="ts">
  import { onMount } from "svelte";

  /**
   * Skip indicator overlay that appears when double-tapping to skip.
   * Shows the skip direction and amount (e.g., "-10s" or "+10s") with a ripple effect.
   */
  export type SkipDirection = "back" | "forward" | null;

  let {
    direction = null as SkipDirection,
    seconds = 10,
    class: className = "",
    onhide = undefined as (() => void) | undefined,
  }: {
    direction: SkipDirection;
    seconds?: number;
    class?: string;
    onhide?: () => void;
  } = $props();

  let isAnimating = $state(false);
  let hideTimer: ReturnType<typeof setTimeout> | null = null;

  // Trigger animation when direction changes
  $effect(() => {
    if (direction) {
      isAnimating = true;

      if (hideTimer) {
        clearTimeout(hideTimer);
      }

      hideTimer = setTimeout(() => {
        isAnimating = false;
        onhide?.();
      }, 600);
    }
  });

  onMount(() => {
    return () => {
      if (hideTimer) {
        clearTimeout(hideTimer);
      }
    };
  });

  let isBack = $derived(direction === "back");
</script>

{#if direction}
  <div
    class="fw-skip-indicator absolute inset-0 z-30 pointer-events-none flex items-center
           {isBack ? 'justify-start pl-8' : 'justify-end pr-8'}
           {className}"
  >
    <!-- Ripple background -->
    <div
      class="absolute top-0 bottom-0 w-1/3 bg-white/10
             {isBack ? 'left-0' : 'right-0'}
             {isAnimating ? 'animate-pulse' : ''}"
    ></div>

    <!-- Skip content -->
    <div
      class="relative flex flex-col items-center gap-1 text-white transition-all duration-200
             {isAnimating ? 'opacity-100 scale-100' : 'opacity-0 scale-75'}"
    >
      <!-- Icon -->
      <div class="flex">
        {#if isBack}
          <svg viewBox="0 0 24 24" fill="currentColor" class="w-8 h-8" aria-hidden="true">
            <path d="M11 18V6l-8.5 6 8.5 6zm.5-6l8.5 6V6l-8.5 6z" />
          </svg>
          <svg viewBox="0 0 24 24" fill="currentColor" class="w-8 h-8 -ml-4" aria-hidden="true">
            <path d="M11 18V6l-8.5 6 8.5 6zm.5-6l8.5 6V6l-8.5 6z" />
          </svg>
        {:else}
          <svg viewBox="0 0 24 24" fill="currentColor" class="w-8 h-8" aria-hidden="true">
            <path d="M4 18l8.5-6L4 6v12zm9-12v12l8.5-6L13 6z" />
          </svg>
          <svg viewBox="0 0 24 24" fill="currentColor" class="w-8 h-8 -ml-4" aria-hidden="true">
            <path d="M4 18l8.5-6L4 6v12zm9-12v12l8.5-6L13 6z" />
          </svg>
        {/if}
      </div>

      <!-- Text -->
      <span class="text-sm font-semibold tabular-nums">
        {isBack ? `-${seconds}s` : `+${seconds}s`}
      </span>
    </div>
  </div>
{/if}
