<script lang="ts">
  import { cn } from "$lib/utils";

  interface Props {
    variant?: "stream" | "clip" | "metric" | "analytics" | "infrastructure" | "generic";
    class?: string;
  }

  let { variant = "generic", class: className }: Props = $props();
</script>

{#if variant === "stream"}
  <!-- Stream Card Skeleton (slab--compact pattern) -->
  <div class={cn("slab slab--compact", className)}>
    <div class="slab-header">
      <div class="flex items-center justify-between w-full">
        <div class="skeleton-text-lg w-2/3"></div>
        <div class="flex items-center space-x-2">
          <div class="skeleton w-16 h-5 rounded-full"></div>
        </div>
      </div>
    </div>

    <div class="slab-body--padded">
      <div class="grid grid-cols-2 gap-4 text-sm">
        <div class="space-y-1">
          <div class="skeleton-text-sm w-12"></div>
          <div class="skeleton-text w-16"></div>
        </div>
        <div class="space-y-1">
          <div class="skeleton-text-sm w-12"></div>
          <div class="skeleton-text w-16"></div>
        </div>
      </div>
    </div>

    <div class="px-4 py-2 border-t border-border/30 bg-muted/20">
      <div class="skeleton-text-sm w-full"></div>
    </div>
  </div>
{:else if variant === "clip"}
  <!-- Clip Card Skeleton (slab--compact pattern) -->
  <div class={cn("slab slab--compact", className)}>
    <div class="slab-header">
      <div class="flex items-center justify-between w-full gap-2">
        <div class="min-w-0 flex-1">
          <div class="skeleton-text-lg w-3/4 mb-1"></div>
          <div class="skeleton-text-sm w-1/2"></div>
        </div>
        <div class="skeleton w-16 h-5 rounded-full"></div>
      </div>
    </div>

    <div class="slab-body--padded">
      <div class="skeleton-text w-full mb-3 h-8"></div>

      <div class="space-y-2 text-sm">
        <div class="flex justify-between">
          <div class="skeleton-text-sm w-16"></div>
          <div class="skeleton-text-sm w-12"></div>
        </div>
        <div class="flex justify-between">
          <div class="skeleton-text-sm w-16"></div>
          <div class="skeleton-text-sm w-12"></div>
        </div>
        <div class="flex justify-between">
          <div class="skeleton-text-sm w-16"></div>
          <div class="skeleton-text-sm w-20"></div>
        </div>
      </div>
    </div>

    <div class="slab-actions">
      <div class="flex justify-center py-2">
        <div class="skeleton-text w-20"></div>
      </div>
    </div>
  </div>
{:else if variant === "metric"}
  <!-- Metric Card Skeleton -->
  <div class={cn("metric-card text-center", className)}>
    <div class="skeleton w-8 h-8 mx-auto mb-3 rounded"></div>
    <div class="skeleton-text-lg w-16 mx-auto mb-1"></div>
    <div class="skeleton-text-sm w-20 mx-auto"></div>
  </div>
{:else if variant === "analytics"}
  <!-- Analytics Card Skeleton -->
  <div class={cn("bg-card p-6 border border-border", className)}>
    <div class="skeleton-text-lg w-48 mb-4"></div>
    <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
      {#each Array(4) as _, index (index)}
        <div class="text-center">
          <div class="skeleton-text-lg w-12 mx-auto mb-1"></div>
          <div class="skeleton-text-sm w-16 mx-auto"></div>
        </div>
      {/each}
    </div>
  </div>
{:else if variant === "infrastructure"}
  <!-- Infrastructure Node/Cluster Card Skeleton (slab--compact pattern) -->
  <div class={cn("slab slab--compact", className)}>
    <div class="slab-header">
      <div class="flex items-center justify-between w-full">
        <div class="skeleton-text-lg w-24"></div>
        <div class="skeleton w-16 h-5 rounded-full"></div>
      </div>
    </div>

    <div class="slab-body--padded">
      <div class="space-y-2">
        <div class="flex justify-between">
          <div class="skeleton-text-sm w-12"></div>
          <div class="skeleton-text-sm w-16"></div>
        </div>
        <div class="flex justify-between">
          <div class="skeleton-text-sm w-16"></div>
          <div class="skeleton-text-sm w-12"></div>
        </div>
        <div class="flex justify-between">
          <div class="skeleton-text-sm w-14"></div>
          <div class="skeleton-text-sm w-10"></div>
        </div>
      </div>
    </div>
  </div>
{:else}
  <!-- Generic Card Skeleton -->
  <div class={cn("skeleton-card", className)}></div>
{/if}
