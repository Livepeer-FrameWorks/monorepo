<script lang="ts">
  import { cn } from "$lib/utils.js";

  let {
    value = 0,
    max = 100,
    class: className = "",
    ...restProps
  }: {
    value?: number;
    max?: number;
    class?: string;
  } & Record<string, unknown> = $props();

  const percentage = $derived(Math.min(100, Math.max(0, (value / max) * 100)));
</script>

<div
  role="progressbar"
  aria-valuenow={value}
  aria-valuemin={0}
  aria-valuemax={max}
  class={cn(
    "relative h-2 w-full overflow-hidden rounded-full bg-muted",
    className
  )}
  {...restProps}
>
  <div
    class="h-full bg-primary transition-all duration-300 ease-in-out"
    style="width: {percentage}%"
  />
</div>
