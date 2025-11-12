<script lang="ts">
  import { healthService } from '$lib/graphql/services/health.js';

  type BufferState = "FULL" | "EMPTY" | "DRY" | "RECOVER" | string;
  type BufferSize = "sm" | "md" | "lg";

  interface Props {
    bufferState?: BufferState;
    bufferHealth?: number | null;
    size?: BufferSize;
  }

  let { bufferState = "EMPTY", bufferHealth = null, size = "md" }: Props = $props();

  let colorClass = $derived(healthService.getBufferStateColor(bufferState));
  let healthPercent = $derived(
    typeof bufferHealth === "number"
      ? Math.round(bufferHealth * 100)
      : null,
  );

  const sizeClasses = {
    sm: "w-4 h-4",
    md: "w-6 h-6",
    lg: "w-8 h-8",
  } as const;

  let sizeClass = $derived(sizeClasses[size] ?? sizeClasses.md);

  function getStateDescription(state: BufferState): string {
    switch (state) {
      case "FULL":
        return "Buffer is full and healthy";
      case "EMPTY":
        return "Buffer has space available";
      case "DRY":
        return "Buffer is critically low";
      case "RECOVER":
        return "Buffer is recovering";
      default:
        return "Buffer state unknown";
    }
  }

  function getStateIcon(state: BufferState): string {
    switch (state) {
      case "FULL":
        return "●●●●";
      case "EMPTY":
        return "●●○○";
      case "DRY":
        return "●○○○";
      case "RECOVER":
        return "●●○○";
      default:
        return "○○○○";
    }
  }
</script>

<div class="flex items-center space-x-2">
  <div class="flex items-center space-x-1">
    <!-- Buffer state icon -->
    <div class={`${colorClass} font-mono ${sizeClass} flex items-center justify-center`}>
      <span class="text-xs">{getStateIcon(bufferState)}</span>
    </div>
    
    <!-- State label -->
    <div class="flex flex-col">
      <span class={`font-medium ${colorClass} text-sm`}>
        {bufferState}
      </span>
      {#if healthPercent !== null}
        <span class="text-xs text-tokyo-night-comment">
          {healthPercent}% healthy
        </span>
      {/if}
    </div>
  </div>

  <!-- Tooltip with description -->
  <div class="group relative">
    <div class="w-4 h-4 rounded-full bg-tokyo-night-selection flex items-center justify-center cursor-help">
      <span class="text-xs text-tokyo-night-comment">?</span>
    </div>
    <div class="absolute bottom-full left-1/2 transform -translate-x-1/2 mb-2 px-2 py-1 bg-tokyo-night-surface text-tokyo-night-fg text-xs rounded shadow-lg opacity-0 group-hover:opacity-100 transition-opacity z-10 whitespace-nowrap">
      {getStateDescription(bufferState)}
    </div>
  </div>
</div>
