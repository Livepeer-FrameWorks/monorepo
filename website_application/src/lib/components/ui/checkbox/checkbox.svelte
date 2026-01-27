<script lang="ts">
  import { Checkbox as CheckboxPrimitive } from "bits-ui";
  import { Check, Minus } from "lucide-svelte";
  import { cn, type WithoutChildrenOrChild } from "$lib/utils";

  let {
    ref = $bindable(null),
    checked = $bindable(false),
    indeterminate = $bindable(false),
    class: className,
    ...restProps
  }: WithoutChildrenOrChild<CheckboxPrimitive.RootProps> = $props();
</script>

<CheckboxPrimitive.Root
  bind:ref
  data-slot="checkbox"
  class={cn(
    "border-input dark:bg-input/30 data-[state=checked]:bg-primary data-[state=checked]:text-primary-foreground dark:data-[state=checked]:bg-primary data-[state=checked]:border-primary focus-visible:border-primary/90 focus-visible:ring-primary/35 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive shadow-[0_2px_8px_rgba(6,15,65,0.12)] peer flex size-4 shrink-0 items-center justify-center rounded-[4px] border outline-none transition-shadow focus-visible:ring-[2px] disabled:cursor-not-allowed disabled:opacity-50",
    className
  )}
  bind:checked
  bind:indeterminate
  {...restProps}
>
  {#snippet children({ checked, indeterminate })}
    <div data-slot="checkbox-indicator" class="text-current transition-none">
      {#if checked}
        <Check class="size-3.5" />
      {:else if indeterminate}
        <Minus class="size-3.5" />
      {/if}
    </div>
  {/snippet}
</CheckboxPrimitive.Root>
