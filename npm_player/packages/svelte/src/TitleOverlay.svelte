<!--
  TitleOverlay.svelte - Title/description overlay at top of player
  Port of src/components/TitleOverlay.tsx
-->
<script lang="ts">
  import { cn } from "@livepeer-frameworks/player-core";

  interface Props {
    title?: string | null;
    description?: string | null;
    isVisible: boolean;
    class?: string;
  }

  let { title = null, description = null, isVisible, class: className = "" }: Props = $props();

  // Don't render if no content
  let hasContent = $derived(!!title || !!description);
</script>

{#if hasContent}
  <div
    class={cn(
      "fw-title-overlay absolute inset-x-0 top-0 z-20 pointer-events-none",
      "bg-gradient-to-b from-black/70 via-black/40 to-transparent",
      "px-4 py-3 transition-opacity duration-300",
      isVisible ? "opacity-100" : "opacity-0",
      className
    )}
  >
    {#if title}
      <h2 class="text-white text-sm font-medium truncate max-w-[80%]">
        {title}
      </h2>
    {/if}
    {#if description}
      <p class="text-white/70 text-xs mt-0.5 line-clamp-2 max-w-[70%]">
        {description}
      </p>
    {/if}
  </div>
{/if}
