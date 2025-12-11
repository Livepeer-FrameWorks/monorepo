<script lang="ts">
  type BufferState = "FULL" | "EMPTY" | "DRY" | "RECOVER" | string | null | undefined;
  type BufferSize = "sm" | "md" | "lg";

  interface Props {
    bufferState?: BufferState;
    bufferHealth?: number | null;
    size?: BufferSize;
    compact?: boolean;
  }

  let { bufferState = "EMPTY", bufferHealth = null, size = "md", compact = false }: Props = $props();

  const normalizedState = $derived(
    typeof bufferState === "string" && bufferState.length > 0 ? bufferState : "UNKNOWN"
  );

  function getBufferStateColor(state: string): string {
    switch (state) {
      case "FULL":
        return "text-success";
      case "EMPTY":
        return "text-warning";
      case "DRY":
        return "text-error";
      case "RECOVER":
        return "text-accent";
      default:
        return "text-muted-foreground";
    }
  }

  function getBufferBgColor(state: string): string {
    switch (state) {
      case "FULL":
        return "bg-success";
      case "EMPTY":
        return "bg-warning";
      case "DRY":
        return "bg-error";
      case "RECOVER":
        return "bg-accent";
      default:
        return "bg-muted-foreground";
    }
  }

  let colorClass = $derived(getBufferStateColor(normalizedState));
  let bgClass = $derived(getBufferBgColor(normalizedState));
  let healthPercent = $derived(
    bufferHealth != null
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

  // Filled dots based on state
  function getFilledCount(state: BufferState): number {
    switch (state) {
      case "FULL":
        return 4;
      case "EMPTY":
        return 2;
      case "DRY":
        return 1;
      case "RECOVER":
        return 2;
      default:
        return 0;
    }
  }

  let filledCount = $derived(getFilledCount(normalizedState));
  let normalizedStateLabel = $derived(normalizedState.toLowerCase());
</script>

{#if compact}
  <!-- Compact mode: just dots indicator with tooltip -->
  <div class="group relative flex items-center gap-1" title={getStateDescription(bufferState)}>
    {#each Array(4) as _, i}
      <div
        class="w-1.5 h-1.5 rounded-full transition-colors {i < filledCount ? bgClass : 'bg-muted-foreground/30'}"
      ></div>
    {/each}
  </div>
{:else}
  <!-- Full mode: dots + label + optional health percent -->
  <div class="flex items-center space-x-2">
    <div class="flex items-center space-x-1.5">
      <!-- Buffer state dots -->
      <div class="flex items-center gap-0.5">
        {#each Array(4) as _, i}
          <div
            class="w-1.5 h-1.5 rounded-full transition-colors {i < filledCount ? bgClass : 'bg-muted-foreground/30'}"
          ></div>
        {/each}
      </div>

      <!-- State label -->
      <span class={`font-medium ${colorClass} text-sm capitalize`}>
        {normalizedStateLabel}
      </span>
    </div>

    {#if healthPercent !== null}
      <span class="text-xs text-muted-foreground">
        ({healthPercent}%)
      </span>
    {/if}
  </div>
{/if}
