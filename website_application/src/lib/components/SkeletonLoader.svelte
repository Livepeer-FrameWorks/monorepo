<script lang="ts">
  import { cn } from "$lib/utils";

  type SkeletonType =
    | "text"
    | "text-sm"
    | "text-lg"
    | "card"
    | "avatar"
    | "chart"
    | "metric"
    | "table-row"
    | "custom";

  interface Props {
    type?: SkeletonType;
    class?: string;
    count?: number;
    inline?: boolean;
  }

  let {
    type = "text",
    class: className,
    count = 1,
    inline = false,
  }: Props = $props();

  const baseClasses: Record<SkeletonType, string> = {
    text: "skeleton-text",
    "text-sm": "skeleton-text-sm",
    "text-lg": "skeleton-text-lg",
    card: "skeleton-card",
    avatar: "skeleton-avatar",
    chart: "skeleton-chart",
    metric: "skeleton-metric",
    "table-row": "skeleton-table-row",
    custom: "skeleton",
  };

  const containerClass = $derived(inline ? "inline-flex" : "space-y-2");
</script>

<div class={containerClass}>
  {#each Array(count) as _, i (i)}
    <div
      class={cn(baseClasses[type], className)}
      style="animation-delay: {i * 0.1}s"
      role="presentation"
      aria-hidden="true"
    ></div>
  {/each}
</div>
