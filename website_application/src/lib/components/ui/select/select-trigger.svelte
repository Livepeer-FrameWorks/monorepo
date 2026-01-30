<script lang="ts">
  import { Select as SelectPrimitive } from "bits-ui";
  import { ChevronDownIcon } from "lucide-svelte";
  import { cn, type WithoutChild } from "$lib/utils";

  let {
    ref = $bindable(null),
    class: className,
    children,
    size = "default",
    ...restProps
  }: WithoutChild<SelectPrimitive.TriggerProps> & {
    size?: "sm" | "default";
  } = $props();
</script>

<SelectPrimitive.Trigger
  bind:ref
  data-slot="select-trigger"
  data-size={size}
  class={cn(
    "border-input/60 data-[placeholder]:text-muted-foreground data-[placeholder]:opacity-60 [&_svg:not([class*='text-'])]:text-muted-foreground focus-visible:border-primary/90 focus-visible:ring-primary/35 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive dark:bg-input/30 dark:hover:bg-input/50 shadow-[0_2px_8px_rgba(6,15,65,0.12)] flex w-full select-none items-center justify-between gap-2 whitespace-nowrap rounded-lg border bg-[hsl(var(--brand-surface)/0.9)] px-4 py-3 text-sm outline-none transition-[color,box-shadow] focus-visible:ring-[2px] disabled:cursor-not-allowed disabled:opacity-50 data-[size=default]:h-auto data-[size=sm]:h-auto *:data-[slot=select-value]:line-clamp-1 *:data-[slot=select-value]:flex *:data-[slot=select-value]:items-center *:data-[slot=select-value]:gap-2 [&_svg:not([class*='size-'])]:size-4 [&_svg]:pointer-events-none [&_svg]:shrink-0",
    className
  )}
  {...restProps}
>
  {@render children?.()}
  <ChevronDownIcon class="size-4 opacity-50" />
</SelectPrimitive.Trigger>
