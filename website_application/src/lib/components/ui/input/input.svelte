<script lang="ts">
  import type { HTMLInputAttributes, HTMLInputTypeAttribute } from "svelte/elements";
  import { cn, type WithElementRef } from "$lib/utils";

  type InputType = Exclude<HTMLInputTypeAttribute, "file">;

  type Props = WithElementRef<
    Omit<HTMLInputAttributes, "type"> &
      ({ type: "file"; files?: FileList } | { type?: InputType; files?: undefined })
  >;

  let {
    ref = $bindable(null),
    value = $bindable(),
    type,
    files = $bindable(),
    class: className,
    "data-slot": dataSlot = "input",
    ...restProps
  }: Props = $props();
</script>

{#if type === "file"}
  <input
    bind:this={ref}
    data-slot={dataSlot}
    class={cn(
      "selection:bg-primary dark:bg-input/30 selection:text-primary-foreground border-input/60 ring-offset-background placeholder:text-muted-foreground placeholder:opacity-60 shadow-[0_2px_8px_rgba(6,15,65,0.12)] flex h-auto w-full min-w-0 rounded-lg border bg-[hsl(var(--brand-surface)/0.9)] px-4 py-3 text-sm font-medium outline-none transition-[color,box-shadow] disabled:cursor-not-allowed disabled:opacity-50",
      "focus-visible:border-primary/90 focus-visible:ring-primary/35 focus-visible:ring-[2px]",
      "aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive",
      className
    )}
    type="file"
    bind:files
    bind:value
    {...restProps}
  />
{:else}
  <input
    bind:this={ref}
    data-slot={dataSlot}
    class={cn(
      "border-input/60 bg-[hsl(var(--brand-surface)/0.9)] selection:bg-primary dark:bg-input/30 selection:text-primary-foreground ring-offset-background placeholder:text-muted-foreground placeholder:opacity-60 shadow-[0_2px_8px_rgba(6,15,65,0.12)] flex h-auto w-full min-w-0 rounded-lg border px-4 py-3 text-base outline-none transition-[color,box-shadow] disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
      "focus-visible:border-primary/90 focus-visible:ring-primary/35 focus-visible:ring-[2px]",
      "aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive",
      className
    )}
    {type}
    bind:value
    {...restProps}
  />
{/if}
