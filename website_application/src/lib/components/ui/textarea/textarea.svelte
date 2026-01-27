<script lang="ts">
  import { cn, type WithElementRef, type WithoutChildren } from "$lib/utils";
  import type { HTMLTextareaAttributes } from "svelte/elements";

  let {
    ref = $bindable(null),
    value = $bindable(),
    class: className,
    "data-slot": dataSlot = "textarea",
    ...restProps
  }: WithoutChildren<WithElementRef<HTMLTextareaAttributes>> = $props();
</script>

<textarea
  bind:this={ref}
  data-slot={dataSlot}
  class={cn(
    "border-input/60 placeholder:text-muted-foreground focus-visible:border-primary/90 focus-visible:ring-primary/35 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive dark:bg-input/30 field-sizing-content shadow-[0_2px_8px_rgba(6,15,65,0.12)] flex min-h-16 w-full rounded-lg border bg-[hsl(var(--brand-surface)/0.9)] px-4 py-3 text-base outline-none transition-[color,box-shadow] focus-visible:ring-[2px] disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
    className
  )}
  bind:value
  {...restProps}
></textarea>
